package worker

import (
	"github.com/golang/glog"
	"github.com/turbonomic/kubeturbo/pkg/discovery/dtofactory"
	"github.com/turbonomic/kubeturbo/pkg/discovery/repository"
	"github.com/turbonomic/kubeturbo/pkg/discovery/stitching"
	"github.com/turbonomic/kubeturbo/pkg/discovery/util"
	"github.com/turbonomic/turbo-go-sdk/pkg/proto"
)

const (
	k8sQuotasWorkerID string = "ResourceQuotasDiscoveryWorker"
)

// Converts the cluster quotaEntity and QuotaMetrics objects to create Quota DTOs
type k8sResourceQuotasDiscoveryWorker struct {
	id         string
	Cluster    *repository.ClusterSummary
	stitchType stitching.StitchingPropertyType
}

func Newk8sResourceQuotasDiscoveryWorker(cluster *repository.ClusterSummary, pType stitching.StitchingPropertyType,
) *k8sResourceQuotasDiscoveryWorker {
	return &k8sResourceQuotasDiscoveryWorker{
		Cluster:    cluster,
		id:         k8sQuotasWorkerID,
		stitchType: pType,
	}
}

func (worker *k8sResourceQuotasDiscoveryWorker) Do(quotaMetricsList []*repository.QuotaMetrics,
) ([]*proto.EntityDTO, error) {
	// Combine quota discovery results from different nodes
	quotaMetricsMap := make(map[string]*repository.QuotaMetrics)

	// combine quota metrics results from different discovery workers
	// each worker will provide the allocation bought for a set of nodes and
	// the allocation used for the pods running on those nodes
	for _, quotaMetrics := range quotaMetricsList {
		glog.V(4).Infof("%s : merging metrics for nodes %s\n",
			quotaMetrics.QuotaName, quotaMetrics.NodeProviders)
		_, exists := quotaMetricsMap[quotaMetrics.QuotaName]
		if !exists {
			quotaMetricsMap[quotaMetrics.QuotaName] = quotaMetrics
		}
		existingMetric := quotaMetricsMap[quotaMetrics.QuotaName]
		// merge the provider node metrics into the existing quota metrics
		for node, nodeMap := range quotaMetrics.AllocationBoughtMap {
			existingMetric.UpdateAllocationBought(node, nodeMap)
		}

		//merge the pod usage from this quota metrics into the existing quota metrics
		existingMetric.UpdateAllocationSold(quotaMetrics.AllocationSold)
	}

	kubeNodes := worker.Cluster.Nodes
	var nodeUIDs []string
	var totalNodeFrequency float64
	var activeNodeCount int = 0
	for _, node := range kubeNodes {
		nodeActive := util.NodeIsReady(node.Node) && util.NodeIsSchedulable(node.Node)
		if nodeActive {
			nodeUIDs = append(nodeUIDs, node.UID)
			totalNodeFrequency += node.NodeCpuFrequency
			activeNodeCount++
		}

	}
	averageNodeFrequency := totalNodeFrequency / float64(activeNodeCount)
	glog.V(2).Infof("Average cluster node cpu frequency in MHz %f\n", averageNodeFrequency)

	// Create the allocation resources for all quota entities using the metrics object
	for quotaName, quotaEntity := range worker.Cluster.QuotaMap {
		// the quota metrics
		quotaMetrics, exists := quotaMetricsMap[quotaName]
		if !exists {
			glog.Errorf("%s : missing allocation metrics for quota\n", quotaName)
			continue
		}
		quotaEntity.AverageNodeCpuFrequency = averageNodeFrequency

		// Bought resources from each node
		// create provider entity for each node
		for _, node := range kubeNodes {
			//Do not include the node that is not ready
			// We still want to include the scheduledisabled nodes in the relationship
			nodeUID := node.UID
			quotaEntity.AddNodeProvider(nodeUID, quotaMetrics.AllocationBoughtMap[nodeUID])
		}

		// Create sold allocation commodity for the types that are not defined in the namespace quota objects
		for resourceType, used := range quotaMetrics.AllocationSold {
			existingResource, _ := quotaEntity.GetResource(resourceType)
			// Check if there is a quota set for this allocation resource
			// If it is set, the allocation usage available from the namespace
			// resource quota object is used
			if quotaEntity.AllocationDefined[resourceType] {
				glog.V(4).Infof("%s::%s : used value available from the quota object, "+
					"existingUsed = %f, pods total usage = %f\n",
					quotaName, resourceType, existingResource.Used, used)
				continue
			} else {
				glog.V(4).Infof("%s::%s : setting usage to pods collection usage, "+
					"existingUsed = %f, pods total usage = %f\n",
					quotaName, resourceType, existingResource.Used, used)
				existingResource.Used = used
			}
		}
	}

	for _, quotaEntity := range worker.Cluster.QuotaMap {
		glog.V(4).Infof("*************** DISCOVERED quota entity %s\n", quotaEntity)
	}

	// Create DTOs for each quota entity
	quotaDtoBuilder := dtofactory.NewQuotaEntityDTOBuilder(worker.Cluster.QuotaMap, worker.Cluster.Nodes, worker.stitchType)
	quotaDtos, _ := quotaDtoBuilder.BuildEntityDTOs()
	return quotaDtos, nil
}
