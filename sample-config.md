# Sample config

I think that seeing a sample way we can configure thigns can make a better idea of 
how things are oging to be, especially with the evaluator. so here is a sample.

```config.yaml
agent:
  collection_interval: 5s

collectors:
  memory:
    enabled: true
    path: /proc/meminfo

alerts:
  - name: high_memory_usage
    metric: memory.used_percent
    operator: ">"
    threshold: 80
    severity: warning
    message: "Memory usage is above 80%"

notifiers:
  console:
    enabled: true
```
