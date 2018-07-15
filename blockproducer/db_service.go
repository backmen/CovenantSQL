/*
 * Copyright 2018 The ThunderDB Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package blockproducer

import (
	"math/rand"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
	"gitlab.com/thunderdb/ThunderDB/conf"
	"gitlab.com/thunderdb/ThunderDB/consistent"
	"gitlab.com/thunderdb/ThunderDB/crypto/asymmetric"
	"gitlab.com/thunderdb/ThunderDB/crypto/hash"
	"gitlab.com/thunderdb/ThunderDB/crypto/kms"
	"gitlab.com/thunderdb/ThunderDB/kayak"
	"gitlab.com/thunderdb/ThunderDB/metric"
	"gitlab.com/thunderdb/ThunderDB/pow/cpuminer"
	"gitlab.com/thunderdb/ThunderDB/proto"
	"gitlab.com/thunderdb/ThunderDB/rpc"
	ct "gitlab.com/thunderdb/ThunderDB/sqlchain/types"
	"gitlab.com/thunderdb/ThunderDB/utils/log"
	wt "gitlab.com/thunderdb/ThunderDB/worker/types"
)

const (
	// MetricFreeMemoryBytes defines metric name for free memory in miner instance.
	MetricFreeMemoryBytes = "node_memory_free_bytes_total"
	// MetricFreeFSBytes defines metric name for free filesystem in miner instance.
	MetricFreeFSBytes = "node_filesystem_free_bytes_total"
	// DefaultAllocationRounds defines max rounds to try allocate peers for database creation.
	DefaultAllocationRounds = 3
	// ServiceName for block producer to provide database management related logic.
	DBServiceName = "BPDB"
)

// DBService defines block producer database service rpc endpoint.
type DBService struct {
	AllocationRounds int
	ServiceMap       *DBServiceMap
	Consistent       *consistent.Consistent
	NodeMetrics      *metric.NodeMetricMap
}

// CreateDatabase defines block producer create database logic.
func (s *DBService) CreateDatabase(req *CreateDatabaseRequest, resp *CreateDatabaseResponse) (err error) {
	// TODO(xq262144), verify identity
	// verify identity

	// create random DatabaseID
	var dbID proto.DatabaseID
	if dbID, err = s.generateDatabaseID(req.GetNodeID()); err != nil {
		return
	}

	// allocate nodes
	var peers *kayak.Peers
	if peers, err = s.allocateNodes(0, dbID, req.ResourceMeta); err != nil {
		return
	}

	// TODO(xq262144), call accounting features, top up deposit
	var genesisBlock *ct.Block
	if genesisBlock, err = s.generateGenesisBlock(dbID, req.ResourceMeta); err != nil {
		return
	}

	defer func() {
		if err != nil {
			// TODO(xq262144), release deposit on error
		}
	}()

	// call miner nodes to provide service
	initSvcReq := &wt.UpdateService{
		Op: wt.CreateDB,
		Instance: wt.ServiceInstance{
			DatabaseID:   dbID,
			Peers:        peers,
			GenesisBlock: genesisBlock,
		},
	}

	rollbackReq := &wt.UpdateService{
		Op: wt.DropDB,
		Instance: wt.ServiceInstance{
			DatabaseID: dbID,
		},
	}

	if err = s.batchSendSvcReq(initSvcReq, rollbackReq, s.peersToNodes(peers)); err != nil {
		return
	}

	// save to meta
	instanceMeta := wt.ServiceInstance{
		DatabaseID:   dbID,
		Peers:        peers,
		ResourceMeta: req.ResourceMeta,
	}

	if err = s.ServiceMap.Set(instanceMeta); err != nil {
		// critical error
		// TODO(xq262144), critical error recover
		return err
	}

	// send response to client
	resp.InstanceMeta = instanceMeta

	return
}

// DropDatabase defines block producer drop database logic.
func (s *DBService) DropDatabase(req *DropDatabaseRequest, resp *DropDatabaseResponse) (err error) {
	// TODO(xq262144), verify identity
	// verify identity and database belonging

	// get database peers
	var instanceMeta wt.ServiceInstance
	if instanceMeta, err = s.ServiceMap.Get(req.DatabaseID); err != nil {
		return
	}

	// call miner nodes to drop database
	dropDBSvcReq := &wt.UpdateService{
		Op: wt.DropDB,
		Instance: wt.ServiceInstance{
			DatabaseID: req.DatabaseID,
		},
	}

	if err = s.batchSendSvcReq(dropDBSvcReq, nil, s.peersToNodes(instanceMeta.Peers)); err != nil {
		return
	}

	// withdraw deposit from sqlchain
	// TODO(xq262144)

	// remove from meta
	if err = s.ServiceMap.Delete(req.DatabaseID); err != nil {
		// critical error
		// TODO(xq262144), critical error recover
		return
	}

	// send response to client
	// nothing to set on response, only error flag

	return
}

// GetDatabase defines block producer get database logic.
func (s *DBService) GetDatabase(req *GetDatabaseRequest, resp *GetDatabaseResponse) (err error) {
	// TODO(xq262144), verify identity
	// verify identity and database belonging

	// fetch from meta
	var instanceMeta wt.ServiceInstance
	if instanceMeta, err = s.ServiceMap.Get(req.DatabaseID); err != nil {
		return
	}

	// send response to client
	resp.InstanceMeta = instanceMeta

	return
}

// GetNodeDatabases defines block producer get node databases logic.
func (s *DBService) GetNodeDatabases(req *wt.InitService, resp *wt.InitServiceResponse) (err error) {
	// fetch from meta
	var instances []wt.ServiceInstance
	if instances, err = s.ServiceMap.GetDatabases(proto.NodeID(req.GetNodeID().String())); err != nil {
		return
	}

	// send response to client
	resp.Instances = instances

	return
}

func (s *DBService) generateDatabaseID(reqNodeID *proto.RawNodeID) (dbID proto.DatabaseID, err error) {
	nonceCh := make(chan cpuminer.NonceInfo)
	quitCh := make(chan struct{})
	miner := cpuminer.NewCPUMiner(quitCh)
	go miner.ComputeBlockNonce(cpuminer.MiningBlock{
		Data:      reqNodeID.CloneBytes(),
		NonceChan: nonceCh,
		Stop:      nil,
	}, cpuminer.Uint256{A: 0, B: 0, C: 0, D: 0}, 4)

	defer close(nonceCh)
	defer close(quitCh)

	for nonce := range nonceCh {
		dbID = proto.DatabaseID(nonce.Hash.String())

		// check existence
		if _, err = s.ServiceMap.Get(dbID); err == ErrNoSuchDatabase {
			err = nil
			return
		}
	}

	return
}

func (s *DBService) allocateNodes(lastTerm uint64, dbID proto.DatabaseID, resourceMeta wt.ResourceMeta) (peers *kayak.Peers, err error) {
	curRange := int(resourceMeta.Node)
	excludeNodes := make(map[proto.NodeID]bool)
	allocated := make([]proto.NodeID, 0)

	if resourceMeta.Node <= 0 {
		err = ErrDatabaseAllocation
		return
	}

	for i := 0; i != s.AllocationRounds; i++ {
		log.Debugf("node allocation round %d", i+1)

		var nodes []proto.Node

		// clear previous allocated
		allocated = allocated[:0]

		nodes, err = s.Consistent.GetNeighbors(string(dbID), curRange)

		log.Debugf("found %d neighbour nodes", len(nodes))

		// TODO(xq262144), brute force implementation to be optimized
		var nodeIDs []proto.NodeID

		for _, node := range nodes {
			if _, ok := excludeNodes[node.ID]; !ok {
				nodeIDs = append(nodeIDs, node.ID)
			}
		}

		log.Debugf("found %d suitable nodes", len(nodeIDs))

		if len(nodeIDs) < int(resourceMeta.Node) {
			continue
		}

		// check node resource status
		metrics := s.NodeMetrics.GetMetrics(nodeIDs)

		log.Debugf("get %d metric records for %d nodes", len(metrics), len(nodeIDs))

		for nodeID, nodeMetric := range metrics {
			var metricValue uint64

			// get metric
			if metricValue, err = s.getMetric(nodeMetric, MetricFreeMemoryBytes); err != nil {
				log.Debugf("get node %s memory metric failed", nodeID)

				// add to excludes
				excludeNodes[nodeID] = true
				continue
			}

			// TODO(xq262144), left reserved resources check is required
			// TODO(xq262144), filesystem check to be implemented

			if resourceMeta.Memory < metricValue {
				// can allocate
				allocated = append(allocated, nodeID)
			} else {
				log.Debugf("node %s memory metric does not meet requirements", nodeID)
				excludeNodes[nodeID] = true
			}
		}

		if len(allocated) >= int(resourceMeta.Node) {
			allocated = allocated[:int(resourceMeta.Node)]

			// build peers
			return s.buildPeers(lastTerm+1, nodes, allocated)
		}

		curRange += int(resourceMeta.Node)
	}

	// allocation failed
	err = ErrDatabaseAllocation
	return
}

func (s *DBService) getMetric(metric metric.MetricMap, key string) (value uint64, err error) {
	var rawMetric *dto.MetricFamily
	var ok bool

	if rawMetric, ok = metric[key]; !ok || rawMetric == nil {
		err = ErrMetricNotCollected
		return
	}

	switch rawMetric.GetType() {
	case dto.MetricType_GAUGE:
		value = uint64(rawMetric.GetMetric()[0].GetGauge().GetValue())
	case dto.MetricType_COUNTER:
		value = uint64(rawMetric.GetMetric()[0].GetCounter().GetValue())
	default:
		err = ErrMetricNotCollected
		return
	}

	return
}

func (s *DBService) buildPeers(term uint64, nodes []proto.Node, allocated []proto.NodeID) (peers *kayak.Peers, err error) {
	// get local private key
	var pubKey *asymmetric.PublicKey
	if pubKey, err = kms.GetLocalPublicKey(); err != nil {
		return
	}

	var privKey *asymmetric.PrivateKey
	if privKey, err = kms.GetLocalPrivateKey(); err != nil {
		return
	}

	// get allocated node info
	allocatedMap := make(map[proto.NodeID]bool)

	for _, nodeID := range allocated {
		allocatedMap[nodeID] = true
	}

	allocatedNodes := make([]proto.Node, 0, len(allocated))

	for _, node := range nodes {
		allocatedNodes = append(allocatedNodes, node)
	}

	peers = &kayak.Peers{
		Term:    term,
		PubKey:  pubKey,
		Servers: make([]*kayak.Server, len(allocated)),
	}

	// TODO(xq262144), more practical leader selection, now random select node as leader
	// random choice leader
	leaderIdx := rand.Intn(len(allocated))

	for idx, node := range allocatedNodes {
		peers.Servers[idx] = &kayak.Server{
			Role:   conf.Follower,
			ID:     node.ID,
			PubKey: node.PublicKey,
		}

		if idx == leaderIdx {
			// set as leader
			peers.Servers[idx].Role = conf.Leader
			peers.Leader = peers.Servers[idx]
		}
	}

	// sign the peers structure
	err = peers.Sign(privKey)

	return
}

func (s *DBService) generateGenesisBlock(dbID proto.DatabaseID, resourceMeta wt.ResourceMeta) (genesisBlock *ct.Block, err error) {
	// TODO(xq262144), following is stub code, real logic should be implemented in the future
	emptyHash := hash.Hash{}

	var pubKey *asymmetric.PublicKey
	if pubKey, err = kms.GetLocalPublicKey(); err != nil {
		return
	}
	var privKey *asymmetric.PrivateKey
	if privKey, err = kms.GetLocalPrivateKey(); err != nil {
		return
	}
	var nodeID proto.NodeID
	if nodeID, err = kms.GetLocalNodeID(); err != nil {
		return
	}

	genesisBlock = &ct.Block{
		SignedHeader: ct.SignedHeader{
			Header: ct.Header{
				Version:     0x01000000,
				Producer:    nodeID,
				GenesisHash: emptyHash,
				ParentHash:  emptyHash,
				Timestamp:   time.Now().UTC(),
			},
			Signee:    pubKey,
			Signature: nil,
		},
	}
	err = genesisBlock.PackAndSignBlock(privKey)

	return
}

func (s *DBService) batchSendSvcReq(req *wt.UpdateService, rollbackReq *wt.UpdateService, nodes []proto.NodeID) (err error) {
	if err = s.batchSendSingleSvcReq(req, nodes); err != nil {
		s.batchSendSingleSvcReq(rollbackReq, nodes)
	}

	return
}

func (s *DBService) batchSendSingleSvcReq(req *wt.UpdateService, nodes []proto.NodeID) (err error) {
	var wg sync.WaitGroup
	errCh := make(chan error, len(nodes))

	for _, node := range nodes {
		wg.Add(1)
		go func(s proto.NodeID, ec chan error) {
			defer wg.Done()
			var resp wt.UpdateServiceResponse
			ec <- rpc.NewCaller().CallNode(s, "DBS.Update", req, &resp)
		}(node, errCh)
	}

	wg.Wait()
	close(errCh)
	err = <-errCh

	return
}

func (s *DBService) peersToNodes(peers *kayak.Peers) (nodes []proto.NodeID) {
	if peers == nil {
		return
	}

	nodes = make([]proto.NodeID, 0, len(peers.Servers))

	for _, s := range peers.Servers {
		nodes = append(nodes, s.ID)
	}

	return
}
