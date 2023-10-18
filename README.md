# kafkakit-prometheus-metricsfetcher

Prometheus metricsfetcher for [kafka-kit](https://github.com/DataDog/kafka-kit).

This fool fetches partition and storage metrics from Prometheus and stores them using original
[format](https://github.com/DataDog/kafka-kit/tree/master/cmd/metricsfetcher#data-structures) in ZooKeeper.


## Motivation

I was looking for a tool that fetches data from Prometheus and imports it to ZooKeeper for [kafka-kit](https://github.com/DataDog/kafka-kit). There is an
[issue](https://github.com/DataDog/kafka-kit/issues/197) in kafka-kit project, from comments I found a project
[BatchLabs/kafkakit-prometheus](https://github.com/BatchLabs/kafkakit-prometheus), unfortunately it fetches data
directly from Prometheus exporters instead of fetching data from Prometheus (it works fine if you have regular hosts 
for Kafka, with Kubernetes it can be a bit more complicated).

Following [BatchLabs/kafkakit-prometheus](https://github.com/BatchLabs/kafkakit-prometheus) example I wrote similar tool.


## Requirements
* Go 1.13 (probably works also with 1.12)
* Prometheus endpoint

## Installation

```
go get github.com/tarvip/kafkakit-prometheus-metricsfetcher
```

## Usage

Configuration can be passed using command line flags or using environment variables.

```
Usage of kafkakit-prometheus-metricsfetcher:
  -broker-id-label string
    	Prometheus label for broker ID [KAFKA_KIT_METRICSFETCHER_BROKER_ID_LABEL] (default "broker_id")
  -broker-id-map string
    	Map value to broker ID. Eg."10.25.76.1=1004,10.53.32.1=1005" [KAFKA_KIT_METRICSFETCHER_BROKER_ID_MAP]
  -broker-storage-query string
    	Prometheus query to get broker storage free space [KAFKA_KIT_METRICSFETCHER_BROKER_STORAGE_QUERY]
  -compress
    	Compress the broker/partition metrics when writing them to ZooKeepeer [KAFKA_KIT_METRICSFETCHER_COMPRESS]
  -dry-run
    	Fetch the metrics but don't write them to ZooKeeper, instead print them [KAFKA_KIT_METRICSFETCHER_DRY_RUN]
  -partition-size-query string
    	Prometheus query to get partition size by topic [KAFKA_KIT_METRICSFETCHER_PARTITION_SIZE_QUERY]
  -prometheus-query-timeout duration
    	Timeout for Prometheus queries [KAFKA_KIT_METRICSFETCHER_PROMETHEUS_QUERY_TIMEOUT] (default 30s)
  -prometheus-url string
    	Prometheus URL [KAFKA_KIT_METRICSFETCHER_PROMETHEUS_URL]
  -zk-addr string
    	Zookeeper host, optional zkchroot after port. Eg. "localhost:2181/my-chroot" [KAFKA_KIT_METRICSFETCHER_ZK_ADDR] (default "localhost:2181")
```

Example:
```
kafkakit-prometheus-metricsfetcher --prometheus-url https://your.prometheus.url \
    --broker-storage-query 'avg(label_replace(kubelet_volume_stats_available_bytes{namespace="kafka",persistentvolumeclaim=~"data-kafka-[0-9]+"},"broker_id","$1","persistentvolumeclaim","data-kafka-([0-9]+)")) by (broker_id)' \
    --partition-size-query 'max(kafka_log_log_size{namespace="kafka"}) by (topic,partition)'
```

`partition-size-query` must have `topic` and `partition` labels.

`broker-storage-query` expects `broker_id` label by default, but it can be changed using `--broker-id-label` flag.


## Additional notes

This tool has no support using authentication when connecting to ZooKeeper, if you have authentication enabled in ZooKeeper, then it might be necessary to create `/topicmappr` node beforehand, otherwise `kafkakit-prometheus-metricsfetcher` may fail with following message:
```
unable to create node /topicmappr. err: zk: not authenticated
```

`/topicmappr` node can be created using `zkCli.sh`:
```
cat > jaas.conf << EOF
Client {
       org.apache.zookeeper.server.auth.DigestLoginModule required
       username="foo"
       password="bar";
};
EOF
```
```
export CLIENT_JVMFLAGS="$CLIENT_JVMFLAGS -Djava.security.auth.login.config=jaas.conf"
```

```
bin/zkCli.sh -server localhost:2181
```
```
[zk: localhost:2181(CONNECTED) 20] create /topicmappr
Created /topicmappr

```
Ensure `/topicmappr` node has correct permissions:
```
[zk: localhost:2181(CONNECTED) 29] getAcl /topicmappr
'world,'anyone
: cdrwa

```

## Acknowledgments

* Thank you authors of [BatchLabs/kafkakit-prometheus](https://github.com/BatchLabs/kafkakit-prometheus)
