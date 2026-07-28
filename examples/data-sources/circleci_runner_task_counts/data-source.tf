data "circleci_runner_task_counts" "example" {
  resource_class = "my-namespace/my-runner"
}

# Queued work that no agent has claimed yet: the signal to scale out.
output "queue_depth" {
  value = data.circleci_runner_task_counts.example.unclaimed_task_count
}

# Work currently in flight: the signal for how much capacity is in use.
output "busy_agents" {
  value = data.circleci_runner_task_counts.example.running_task_count
}
