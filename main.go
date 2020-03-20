package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/samuel/go-zookeeper/zk"
	"github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
)

type brokerStorageFreeValue struct {
	StorageFree float64
}
type brokerStorageFree map[string]brokerStorageFreeValue

type partitionSizeValue struct {
	Size float64
}
type partitionSize map[string]partitionSizeValue
type topicPartitionSize map[string]partitionSize

var (
	log *logrus.Logger

	flPrometheusURL          string
	flPrometheusQueryTimeout time.Duration
	flZkAddr                 string
	flPartitionSizeQuery     string
	flBrokerStorageQuery     string
	flBrokerIDLabel          string
	flDryRun                 bool

	zkChroot  string
	apiClient api.Client
)

func init() {
	// Setup logging
	log = logrus.New()
	log.SetFormatter(&logrus.TextFormatter{
		DisableColors:   true,
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02T15:04:05.000Z",
	})
	log.SetOutput(os.Stdout)

	flag.StringVar(&flPrometheusURL, "prometheus-url", "", "Prometheus URL")
	flag.DurationVar(&flPrometheusQueryTimeout, "prometheus-query-timeout", 30*time.Second, "Timeout for Prometheus queries")
	flag.StringVar(&flZkAddr, "zk-addr", "zookeeper:2181", "Zookeeper host")
	flag.StringVar(&flPartitionSizeQuery, "partition-size-query", "", "Prometheus query to get partition size by topic")
	flag.StringVar(&flBrokerStorageQuery, "broker-storage-query", "", "Prometheus query to get broker storage free space")
	flag.StringVar(&flBrokerIDLabel, "broker-id-label", "broker_id", "Prometheus label for broker ID")
	flag.BoolVar(&flDryRun, "dry-run", false, "Fetch the metrics but don't write them to ZooKeeper, instead print them")
	flag.Parse()
}

func promQuery(q string) (model.Value, error) {
	ctx, cancel := context.WithTimeout(context.Background(), flPrometheusQueryTimeout)
	defer cancel()

	v1api := v1.NewAPI(apiClient)
	result, warnings, err := v1api.Query(ctx, q, time.Now())

	if err != nil {
		return nil, err
	}

	if len(warnings) > 0 {
		log.Warning(warnings)
	}

	return result, nil
}

func getBrokerFreeSpace() *brokerStorageFree {
	m := make(brokerStorageFree)

	result, err := promQuery(flBrokerStorageQuery)
	if err != nil {
		log.Fatalf("Error getting broker storage free space from Prometheus: %v", err)
	}

	if result.Type() == model.ValVector {
		vectorVal := result.(model.Vector)

		for _, elem := range vectorVal {
			bid := string(elem.Metric[model.LabelName(flBrokerIDLabel)])
			m[bid] = brokerStorageFreeValue{StorageFree: float64(elem.Value)}
		}
	}

	return &m
}

func getPartitionSizes() *topicPartitionSize {
	m := make(topicPartitionSize)

	result, err := promQuery(flPartitionSizeQuery)
	if err != nil {
		log.Errorf("Error getting partition sizes from Prometheus: %v", err)
		os.Exit(1)
	}

	if result.Type() == model.ValVector {
		vectorVal := result.(model.Vector)

		for _, elem := range vectorVal {
			topic := string(elem.Metric["topic"])
			partition := string(elem.Metric["partition"])

			v, ok := m[topic]
			if !ok {
				v = make(partitionSize)
			}

			v[partition] = partitionSizeValue{Size: float64(elem.Value)}
			m[topic] = v
		}
	}

	return &m
}

func processData(zkConn *zk.Conn, brokerMetrics *brokerStorageFree, partitionMapping *topicPartitionSize) error {
	defer zkConn.Close()

	topicPartitionSizeData, err := json.Marshal(*partitionMapping)
	if err != nil {
		return err
	}

	brokerMetricsData, err := json.Marshal(*brokerMetrics)
	if err != nil {
		return err
	}

	switch {
	case flDryRun:
		// In dry-run don't do anything but display the information we retrieved and computed.
		log.Println("partition mapping")

		for topic, m := range *partitionMapping {
			log.Printf("topic: %s", topic)

			type el struct {
				Partition int
				Size      uint64
			}

			var entries []el

			for partition, obj := range m {
				p, _ := strconv.Atoi(partition)

				entries = append(entries, el{
					Partition: p,
					Size:      uint64(obj.Size),
				})
			}

			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Partition < entries[j].Partition
			})

			for _, entry := range entries {
				log.Printf("partition %-4d size: %s", entry.Partition, humanize.Bytes(entry.Size))
			}
		}

		log.Println("fetched metrics")

		for brokerID, obj := range *brokerMetrics {
			log.Printf("broker #%-4s %15s: %s", brokerID, "storage free", humanize.Bytes(uint64(obj.StorageFree)))
		}

	default:
		if err := writeToZookeeper(zkConn, "partitionmeta", topicPartitionSizeData); err != nil {
			return err
		}

		if err := writeToZookeeper(zkConn, "brokermetrics", brokerMetricsData); err != nil {
			return err
		}
	}

	return nil
}

func writeToZookeeper(zkConn *zk.Conn, path string, data []byte) error {
	const root = "/topicmappr"

	// If our cluster is a zk chroot we need to use it too.

	var dir string
	if zkChroot != "" {
		dir = zkChroot + root
	} else {
		dir = root
	}

	path = dir + "/" + path

	// Remove the old node.
	err := zkConn.Delete(path, 0)
	if err != nil && err != zk.ErrNoNode {
		return fmt.Errorf("unable to delete path %s. err: %v", path, err)
	}

	acl, _, err := zkConn.GetACL(dir)
	if err != nil {
		if err == zk.ErrNoNode {
			// Create the directory node if it is missing
			_, err = zkConn.Create(dir, nil, 0, zk.WorldACL(zk.PermAll))
			if err != nil && err != zk.ErrNodeExists {
				return fmt.Errorf("unable to create node %s. err: %v", dir, err)
			}
		} else {
			return fmt.Errorf("unable to get node %s acl. err: %v", dir, err)
		}
	} else {
		// Ensure that we have WorldACL with PermAll
		var waclExists bool
		wacl := zk.WorldACL(zk.PermAll)[0]
		for _, a := range acl {
			if a == wacl {
				waclExists = true
				break
			}
		}
		if !waclExists {
			return fmt.Errorf("zookeeper node %s has wrong ACL: %v", dir, acl)
		}
	}

	// Create the data node
	log.Printf("writing data to %s", path)

	_, err = zkConn.Create(path, data, 0, zk.WorldACL(zk.PermAll))
	if err != nil {
		return fmt.Errorf("unable to create path %s. err: %v", path, err)
	}

	return nil
}

func main() {
	// Prometheus client
	if flPrometheusURL == "" {
		log.Fatal("Please provide prometheus-url")
	}

	var err error
	apiClient, err = api.NewClient(api.Config{
		Address: flPrometheusURL,
	})

	if err != nil {
		log.Fatalf("Error creating Prometheus client: %v", err)
	}

	// Zookeeper connection
	if flZkAddr == "" {
		log.Fatal("please provide the zookeeper host with --zk-addr")
	}

	var zkAddr string
	if pos := strings.IndexByte(flZkAddr, '/'); pos >= 0 {
		zkAddr = flZkAddr[:pos]
		zkChroot = flZkAddr[pos:]
	} else {
		zkAddr = flZkAddr
	}

	zk.DefaultLogger = log.WithField("logger", "zk")
	zkConn, _, err := zk.Connect([]string{zkAddr}, 20*time.Second)

	if err != nil {
		log.Fatalf("Error creating zookeeper connection: %v", err)
	}

	// Get data
	brokerMetrics := getBrokerFreeSpace()
	partitionMapping := getPartitionSizes()

	err = processData(zkConn, brokerMetrics, partitionMapping)
	if err != nil {
		log.Fatalf("Failed to process data: %v", err)
	}
}
