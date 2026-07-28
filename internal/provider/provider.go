// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/CircleCI-Public/circleci-sdk-go/client"
	ccicontext "github.com/CircleCI-Public/circleci-sdk-go/context"
	"github.com/CircleCI-Public/circleci-sdk-go/envcontext"
	"github.com/CircleCI-Public/circleci-sdk-go/envproject"
	"github.com/CircleCI-Public/circleci-sdk-go/organization"
	"github.com/CircleCI-Public/circleci-sdk-go/pipeline"
	"github.com/CircleCI-Public/circleci-sdk-go/project"
	"github.com/CircleCI-Public/circleci-sdk-go/runner"
	"github.com/CircleCI-Public/circleci-sdk-go/trigger"
	"github.com/CircleCI-Public/circleci-sdk-go/webhook"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"terraform-provider-circleci/internal/circleci"
)

// Ensure CircleCiProvider satisfies various provider interfaces.
var _ provider.Provider = &CircleCiProvider{}
var _ provider.ProviderWithFunctions = &CircleCiProvider{}
var _ provider.ProviderWithEphemeralResources = &CircleCiProvider{}

// circleciClientWrapper wraps all the services provided by the circleci API client.
//
// Client is the provider's own API client and is what new resources must use. The
// circleci-sdk-go services below it are legacy: they are retained for the
// resources written against them and shrink as those are migrated. See
// internal/httpcl/DIVERGENCES.md for why.
type CircleCiClientWrapper struct {
	Client *circleci.Client

	ContextService                    *ccicontext.ContextService
	EnvironmentVariableService        *envcontext.EnvService
	ProjectService                    *project.ProjectService
	PipelineService                   *pipeline.PipelineService
	OrganizationService               *organization.OrganizationService
	TriggerService                    *trigger.TriggerService
	WebhookService                    *webhook.WebhookService
	ProjectEnvironmentVariableService *envproject.EnvService
	RunnerService                     *runner.Service
}

// circleciProviderModel maps provider schema data to a Go type.
type circleciProviderModel struct {
	Host       types.String `tfsdk:"host"`
	Key        types.String `tfsdk:"key"`
	RunnerHost types.String `tfsdk:"runner_host"`
	Deployment types.String `tfsdk:"deployment"`
}

// CircleCiProvider defines the provider implementation.
type CircleCiProvider struct {
	// version is set to the provider version on release, "dev" when the
	// provider is built and ran locally, and "test" when running acceptance
	// testing.
	version string
}

func (p *CircleCiProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "circleci"
	resp.Version = p.version
}

func (p *CircleCiProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage CircleCI organizations, projects, pipelines and settings as code. " +
			"Works against CircleCI Cloud and CircleCI Server; see the `deployment` attribute.",
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "CircleCI API host, as a bare origin such as `https://circleci.com`. " +
					"For CircleCI Server, use your installation's hostname. " +
					"May also be set with the `CIRCLE_HOST` environment variable. " +
					"Defaults to `" + circleci.DefaultHost + "`.\n\n" +
					"A trailing `/api/v2` is accepted and stripped, for compatibility with " +
					"provider versions that documented the host that way.",
			},
			"key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "CircleCI API token (a personal access token). " +
					"May also be set with the `CIRCLE_TOKEN` environment variable.",
			},
			"runner_host": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Host serving the self-hosted runner API. On CircleCI Cloud this " +
					"API lives on a separate origin and defaults to `https://runner.circleci.com`. " +
					"On CircleCI Server it is served by your installation, so set this to your " +
					"Server hostname. May also be set with the `CIRCLE_RUNNER_HOST` environment variable.",
			},
			"deployment": schema.StringAttribute{
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf(string(circleci.DeploymentCloud), string(circleci.DeploymentServer)),
				},
				MarkdownDescription: "Which kind of CircleCI installation `host` refers to: " +
					"`cloud` (the default) or `server`.\n\n" +
					"This selects the API version used for each resource. CircleCI Server does not " +
					"route the v3 API, so setting `server` makes the provider use v2 throughout. " +
					"Resources that exist only on v3 are unavailable when this is `server` and will " +
					"report a clear error rather than a confusing HTTP 404. " +
					"May also be set with the `CIRCLE_DEPLOYMENT` environment variable.",
			},
		},
	}
}

func (p *CircleCiProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	// Retrieve provider data from configuration
	var config circleciProviderModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// If practitioner provided a configuration value for any of the
	// attributes, it must be a known value.
	if config.Key.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("key"),
			"Unknown CircleCI API Key",
			"The provider cannot create the CircleCI API client as there is an unknown configuration value for the CircleCI API key. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the CIRCLE_TOKEN environment variable.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	// Default values to environment variables, but override
	// with Terraform configuration value if set.

	host := os.Getenv("CIRCLE_HOST")
	key := os.Getenv("CIRCLE_TOKEN")
	runner_host := os.Getenv("CIRCLE_RUNNER_HOST")
	deployment := os.Getenv("CIRCLE_DEPLOYMENT")

	if !config.Host.IsNull() {
		host = config.Host.ValueString()
	}

	if !config.Key.IsNull() {
		key = config.Key.ValueString()
	}

	if !config.RunnerHost.IsNull() {
		runner_host = config.RunnerHost.ValueString()
	}

	if !config.Deployment.IsNull() {
		deployment = config.Deployment.ValueString()
	}

	if deployment == "" {
		deployment = string(circleci.DeploymentCloud)
	}

	// The API version is no longer part of the host: each client appends its own
	// /api/vN prefix, which is what lets v3 be used on Cloud and v2 on Server.
	// Earlier releases documented host as "https://circleci.com/api/v2", so strip
	// that suffix and tell the practitioner rather than producing /api/v2/api/v2.
	origin, hadVersionSuffix := circleci.NormalizeHost(host)
	if hadVersionSuffix {
		resp.Diagnostics.AddAttributeWarning(
			path.Root("host"),
			"CircleCI host includes an API version suffix",
			fmt.Sprintf(
				"The configured host %q includes an API version path. The provider now expects a bare "+
					"origin and selects the API version per request, so %q was used instead. "+
					"Update the configuration to %q to silence this warning.",
				host, origin, origin,
			),
		)
	}

	if origin == "" {
		origin = circleci.DefaultHost
	}

	// If any of the expected configurations are missing, return
	// errors with provider-specific guidance.
	if key == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("key"),
			"Missing CircleCI API Password",
			"The provider cannot create the CircleCI API client as there is a missing or empty value for the CircleCI API password. "+
				"Set the password value in the configuration or use the CIRCLE_TOKEN environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	// The provider's own client. New resources use this.
	ownClient := circleci.New(circleci.Config{
		Host:       origin,
		Token:      key,
		Deployment: circleci.Deployment(deployment),
		RunnerHost: runner_host,
		UserAgent:  "terraform-provider-circleci/" + p.version,
	})

	// Legacy circleci-sdk-go client. Its base URL must carry the version, since
	// the SDK builds request paths relative to it.
	circleciClient := client.NewClient(origin+"/api/v2", key)
	contextService := ccicontext.NewContextService(circleciClient)
	organizationService := organization.NewOrganizationService(circleciClient)
	projectService := project.NewProjectService(circleciClient)
	pipelineService := pipeline.NewPipelineService(circleciClient)
	environmentVariableService := envcontext.NewEnvService(circleciClient)
	triggerService := trigger.NewTriggerService(circleciClient)
	webhookService := webhook.NewWebhookService(circleciClient)
	projectEnvVarService := envproject.NewEnvService(circleciClient)
	var runnerService *runner.Service
	if runner_host == "" {
		runnerService = runner.NewService(circleciClient)
	} else {
		runnerService = runner.NewServiceWithBaseURL(circleciClient, runner_host)
	}
	// TODO: would it be possible to verify that the client is correctly configured?

	// Make the CircleCI client available during DataSource and Resource type Configure methods.
	cccw := CircleCiClientWrapper{
		Client:                            ownClient,
		ContextService:                    contextService,
		EnvironmentVariableService:        environmentVariableService,
		OrganizationService:               organizationService,
		ProjectService:                    projectService,
		PipelineService:                   pipelineService,
		TriggerService:                    triggerService,
		WebhookService:                    webhookService,
		ProjectEnvironmentVariableService: projectEnvVarService,
		RunnerService:                     runnerService,
	}
	resp.DataSourceData = &cccw
	resp.ResourceData = &cccw
}

func (p *CircleCiProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewProjectResource,
		NewPipelineResource,
		NewTriggerResource,
		NewContextResource,
		NewContextRestrictionResource,
		NewContextEnvironmentVariableResource,
		NewWebhookResource,
		NewOrganizationResource,
		NewProjectEnvironmentVariableResource,
		NewRunnerResourceClassResource,
		NewRunnerTokenResource,

		// Group access control. Available on CircleCI Cloud and CircleCI Server,
		// except circleci_project_group: the project-group route is not exposed
		// by a Server installation.
		NewCheckoutKeyResource,
		NewProjectSettingsResource,
		NewGroupResource,
		NewGroupMembershipResource,
		NewProjectGroupResource,
		NewURLOrbAllowListEntryResource,

		// CircleCI Cloud only: backed by the v3 API, which Server does not route.
		NewOrganizationSettingsResource,
	}
}

func (p *CircleCiProvider) EphemeralResources(ctx context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{}
}

func (p *CircleCiProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewProjectDataSource,
		NewProjectSettingsDataSource,
		NewPipelineDataSource,
		NewTriggerDataSource,
		NewContextDataSource,
		NewContextEnvironmentVariableDataSource,
		NewWebhookDataSource,
		NewOrganizationDataSource,
		NewProjectEnvironmentVariableDataSource,
		NewRunnerResourceClassDataSource,

		NewCheckoutKeysDataSource,
		NewRunnersDataSource,
		NewRunnerTokensDataSource,
		NewRunnerResourceClassesDataSource,
		NewRunnerTaskCountsDataSource,
		NewGroupDataSource,
		NewGroupsDataSource,
		NewGroupMembershipDataSource,
		NewProjectGroupsDataSource,
		NewURLOrbAllowListDataSource,

		// CircleCI Cloud only: backed by the v3 API, which Server does not route.
		NewOrganizationSettingsDataSource,
	}
}

func (p *CircleCiProvider) Functions(ctx context.Context) []func() function.Function {
	return []func() function.Function{}
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &CircleCiProvider{
			version: version,
		}
	}
}
