## What is our agent and what will it be doing?

Our agent is what will be running on the host machine we are monitoring. The agent handles the entirety of the system. From monitoring the various things to producing usable metrics and also sending out alerts when things go out of thresholds.

Agent will run as a daemon, collecting metrics.

## Collectors
Collectors are the components that collect data from the host machine. They are responsible for reading the source data and transforming it into a format that can be used by the agent. There are many kinds of collectors, those collecting values from say the cpu, memory, disks, potentially networks and also docker daemon.

Once these collectors have collected data, they have to transform it into a format that can be used downstream. 

These collectors should be the only thing that knows about the low-level constructs of the data source, once they have collected the data, they should return it in a shape that can be used by downstream components without having to know anything about the data source.


## Evaluator
Evaluator is the component that reads the incoming samples from the collectors and then evaluates them against the configured thresholds. If a threshold is crossed, it will create an alert. It should also be aware of some metrics that make sense only when compared against previous values, eg, cpu usage are only meaningful when compared against previous values.

## Alert Manager
Alert Manager is the component that receives alerts from the evaluator and then handles them. It is responsible for sending out notifications to the appropriate channels (email, slack, etc.) and making sure we don't oversend or have any issues that occur during the notification process
