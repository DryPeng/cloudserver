package slave

import (
	"bytes"
	"encoding/gob"
	"fmt"
	model "github.com/cloudreve/Cloudreve/v3/models"
	"github.com/cloudreve/Cloudreve/v3/pkg/aria2/common"
	"github.com/cloudreve/Cloudreve/v3/pkg/aria2/rpc"
	"github.com/cloudreve/Cloudreve/v3/pkg/cluster"
	"github.com/cloudreve/Cloudreve/v3/pkg/mq"
	"github.com/cloudreve/Cloudreve/v3/pkg/request"
	"github.com/cloudreve/Cloudreve/v3/pkg/serializer"
	"github.com/cloudreve/Cloudreve/v3/pkg/task"
	"net/http"
	"net/url"
	"sync"
)

var DefaultController Controller

// Controller controls communications between master and slave
type Controller interface {
	// Handle heartbeat sent from master
	HandleHeartBeat(*serializer.NodePingReq) (serializer.NodePingResp, error)

	// Get Aria2 Instance by master node ID
	GetAria2Instance(string) (common.Aria2, error)

	// Send event change message to master node
	SendNotification(string, string, mq.Message) error

	// Submit async task into task pool
	SubmitTask(string, task.Job, string) error

	// Get master node info
	GetMasterInfo(string) (*MasterInfo, error)
}

type slaveController struct {
	masters map[string]MasterInfo
	client  request.Client
	lock    sync.RWMutex
}

// info of master node
type MasterInfo struct {
	SlaveID uint
	ID      string
	TTL     int
	URL     *url.URL
	// used to invoke aria2 rpc calls
	Instance cluster.Node

	jobTracker map[string]bool
}

func Init() {
	DefaultController = &slaveController{
		masters: make(map[string]MasterInfo),
		client:  request.NewClient(),
	}
	gob.Register(rpc.StatusInfo{})
}

func (c *slaveController) HandleHeartBeat(req *serializer.NodePingReq) (serializer.NodePingResp, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	req.Node.AfterFind()

	// close old node if exist
	origin, ok := c.masters[req.SiteID]

	if (ok && req.IsUpdate) || !ok {
		if ok {
			origin.Instance.Kill()
		}

		masterUrl, err := url.Parse(req.SiteURL)
		if err != nil {
			return serializer.NodePingResp{}, err
		}

		c.masters[req.SiteID] = MasterInfo{
			SlaveID:    req.Node.ID,
			ID:         req.SiteID,
			URL:        masterUrl,
			TTL:        req.CredentialTTL,
			jobTracker: make(map[string]bool),
			Instance: cluster.NewNodeFromDBModel(&model.Node{
				MasterKey:              req.Node.MasterKey,
				Type:                   model.MasterNodeType,
				Aria2Enabled:           req.Node.Aria2Enabled,
				Aria2OptionsSerialized: req.Node.Aria2OptionsSerialized,
			}),
		}
	}

	return serializer.NodePingResp{}, nil
}

func (c *slaveController) GetAria2Instance(id string) (common.Aria2, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	if node, ok := c.masters[id]; ok {
		return node.Instance.GetAria2Instance(), nil
	}

	return nil, ErrMasterNotFound
}

func (c *slaveController) SendNotification(id, subject string, msg mq.Message) error {
	c.lock.RLock()

	if node, ok := c.masters[id]; ok {
		c.lock.RUnlock()

		apiPath, _ := url.Parse(fmt.Sprintf("/api/v3/slave/notification/%s", subject))
		body := bytes.Buffer{}
		enc := gob.NewEncoder(&body)
		if err := enc.Encode(&msg); err != nil {
			return err
		}

		res, err := c.client.Request(
			"PUT",
			node.URL.ResolveReference(apiPath).String(),
			&body,
			request.WithHeader(http.Header{"X-Node-Id": []string{fmt.Sprintf("%d", node.SlaveID)}}),
			request.WithCredential(node.Instance.MasterAuthInstance(), int64(node.TTL)),
		).CheckHTTPResponse(200).DecodeResponse()
		if err != nil {
			return err
		}

		if res.Code != 0 {
			return serializer.NewErrorFromResponse(res)
		}

		return nil
	}

	c.lock.RUnlock()
	return ErrMasterNotFound
}

// SubmitTask 提交异步任务
func (c *slaveController) SubmitTask(id string, job task.Job, hash string) error {
	c.lock.RLock()
	defer c.lock.RUnlock()

	if node, ok := c.masters[id]; ok {
		if _, ok := node.jobTracker[hash]; ok {
			// 任务已存在，直接返回
			return nil
		}

		task.TaskPoll.Submit(job)
		return nil
	}

	return ErrMasterNotFound
}

// GetMasterInfo 获取主机节点信息
func (c *slaveController) GetMasterInfo(id string) (*MasterInfo, error) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	if node, ok := c.masters[id]; ok {
		return &node, nil
	}

	return nil, ErrMasterNotFound
}
