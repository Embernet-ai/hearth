# Watching Hearth

Two files and a scrape. Nothing here is Hearth-specific plumbing: it is the
same Prometheus and the same Grafana the rest of the estate already runs, which
is the point. A product with its own monitoring stack is a second thing to keep
up at three in the morning.

## The scrape

`GET /metrics` on any replica, in the Prometheus text format, no authentication.
It carries counts and gauges and nothing about anybody: no titles, no
identifiers, no paths people asked for. Routes are the patterns the mux
matched, so the label set is fixed and a scan for URLs cannot turn one counter
into a hundred thousand series.

It is deliberately outside the rate limiter. A scrape every fifteen seconds
must never be the thing that gets throttled, and it reads counters rather than
the database.

```yaml
- job_name: hearth
  kubernetes_sd_configs:
    - role: pod
      namespaces: { names: [hearth] }
  relabel_configs:
    - source_labels: [__meta_kubernetes_pod_label_app]
      action: keep
      regex: hearth
    - source_labels: [__meta_kubernetes_pod_name]
      target_label: instance
```

## The dashboard

`hearth-dashboard.json`, imported into Grafana. Three rows: whether it is
answering, whether the background work is moving, and where the time goes by
route.

Two panels are worth explaining because they are the ones that catch real
problems rather than looking busy.

**Queue depth.** A depth that climbs and never comes back down is a replica
with `HEARTH_WORKER_ENABLED` off. That deployment looks completely healthy from
outside: every page loads, every write succeeds, and nothing is indexed,
notified, or delivered.

**Unpublished events.** Rows written to the outbox that the relay has not
handed to the queue. Climbing means the relay has stopped and nothing
downstream knows anything has changed, which is the same silence as everything
being quiet.

## Latency is a mean, and says so

There is no histogram here, so there is no p99. The panel divides total time by
request count and is labelled a mean. A percentile computed from a counter
would be a made-up number wearing a name people trust, and the honest version
is more useful than that: on a workspace this size, a mean that moves is a mean
worth looking at.

## Traces

Every response carries `traceparent` and `X-Trace-Id`, and every log line
carries the same id. Following one request through the ingress, the
application, and the worker that finished its job is a grep for that id. There
is no collector and there are no spans; see the comment at the top of
`internal/platform/httpx/trace.go` for what that trade is and when it stops
being the right one.
