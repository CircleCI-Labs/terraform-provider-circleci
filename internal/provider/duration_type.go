// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// durationType is a string attribute type that compares its values as durations
// rather than as text.
//
// It exists because CircleCI reformats the durations it stores. the API
// formats a TTL with time.Duration.String(), so a configured "1h" is returned as
// "1h0m0s" and "90m" as "1h30m0s". With a plain string attribute that produces
// "Provider produced inconsistent result after apply" on create and a diff on
// every subsequent plan, even though nothing has changed.
//
// Normalizing in the provider is not sufficient on its own: after
// `terraform import` there is no configuration to normalize towards, so state
// holds the API's spelling while the configuration holds the author's, and the
// next plan shows a spurious change. Semantic equality resolves both cases,
// because "1h" and "1h0m0s" genuinely denote the same duration.
type durationType struct {
	basetypes.StringType
}

var (
	_ basetypes.StringTypable                    = durationType{}
	_ basetypes.StringValuableWithSemanticEquals = durationValue{}
)

func (t durationType) String() string { return "provider.durationType" }

func (t durationType) Equal(o attr.Type) bool {
	other, ok := o.(durationType)
	if !ok {
		return false
	}

	return t.StringType.Equal(other.StringType)
}

func (t durationType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return durationValue{StringValue: in}, nil
}

func (t durationType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}

	stringValue, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, errUnexpectedStringValue
	}

	value, diags := t.ValueFromString(ctx, stringValue)
	if diags.HasError() {
		return nil, errUnexpectedStringValue
	}

	return value, nil
}

func (t durationType) ValueType(context.Context) attr.Value {
	return durationValue{}
}

// durationValue is a string value whose equality is duration-aware.
type durationValue struct {
	basetypes.StringValue
}

func (v durationValue) Type(context.Context) attr.Type { return durationType{} }

func (v durationValue) Equal(o attr.Value) bool {
	other, ok := o.(durationValue)
	if !ok {
		return false
	}

	return v.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals reports whether two spellings denote the same duration.
//
// A value that does not parse falls back to exact string comparison, so an
// invalid duration still produces a diff rather than being silently accepted.
func (v durationValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	other, ok := newValuable.(durationValue)
	if !ok {
		return false, diags
	}

	if v.IsNull() || v.IsUnknown() || other.IsNull() || other.IsUnknown() {
		return v.StringValue.Equal(other.StringValue), diags
	}

	mine, err := time.ParseDuration(v.ValueString())
	if err != nil {
		return v.ValueString() == other.ValueString(), diags
	}

	theirs, err := time.ParseDuration(other.ValueString())
	if err != nil {
		return v.ValueString() == other.ValueString(), diags
	}

	return mine == theirs, diags
}

// newDurationValue wraps a plain string, mapping the empty string to null so an
// absent duration does not read back as an empty one.
func newDurationValue(s string) durationValue {
	if s == "" {
		return durationValue{StringValue: basetypes.NewStringNull()}
	}

	return durationValue{StringValue: basetypes.NewStringValue(s)}
}

// errUnexpectedStringValue is returned when the framework hands durationType a
// value that is not a string, which would be a bug in the framework rather than
// in a configuration.
var errUnexpectedStringValue = errors.New("expected a string value for a duration attribute")
