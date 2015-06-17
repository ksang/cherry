/*
 * Cherry - An OpenFlow Controller
 *
 * Copyright (C) 2015 Samjung Data Service Co., Ltd.,
 * Kitae Kim <superkkt@sds.co.kr>
 */

package network

import (
	"fmt"
	"git.sds.co.kr/cherry.git/cherryd/graph"
	"git.sds.co.kr/cherry.git/cherryd/internal/log"
	"net"
	"sync"
)

type Watcher interface {
	DeviceAdded(*Device)
	DeviceLinked([2]*Port)
	DeviceRemoved(*Device)
	NodeAdded(*Node)
	PortRemoved(*Port)
}

type Finder interface {
	Device(id string) *Device
	Devices() []*Device
	// IsEnabledBySTP returns whether p is disabled by spanning tree protocol
	IsEnabledBySTP(p *Port) bool
	// IsEdge returns whether p is an edge among two switches
	IsEdge(p *Port) bool
	Node(mac net.HardwareAddr) *Node
	Path(srcDeviceID, dstDeviceID string) [][2]*Port
}

type Topology struct {
	mutex sync.RWMutex
	// Key is IP address of a device
	devices map[string]*Device
	// Key is MAC address of a node
	nodes map[string]*Node
	log   log.Logger
	graph *graph.Graph
}

func NewTopology(log log.Logger) *Topology {
	return &Topology{
		devices: make(map[string]*Device),
		nodes:   make(map[string]*Node),
		log:     log,
		graph:   graph.New(),
	}
}

func (r *Topology) Devices() []*Device {
	// Read lock
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	v := make([]*Device, 0)
	for _, d := range r.devices {
		v = append(v, d)
	}

	return v
}

// Device may return nil if a device whose ID is id does not exist
func (r *Topology) Device(id string) *Device {
	// Read lock
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	return r.devices[id]
}

func (r *Topology) removeAllNodes(d *Device) {
	ports := d.Ports()
	for _, p := range ports {
		for _, n := range p.Nodes() {
			delete(r.nodes, n.MAC().String())
			p.RemoveNode(n.MAC())
		}
	}
}

func (r *Topology) DeviceAdded(d *Device) {
	// Write lock
	r.mutex.Lock()
	defer r.mutex.Unlock()

	r.devices[d.ID()] = d
	r.graph.AddVertex(d)
}

func (r *Topology) DeviceRemoved(d *Device) {
	// Write lock
	r.mutex.Lock()
	defer r.mutex.Unlock()

	id := d.ID()
	// Device exists?
	d, ok := r.devices[id]
	if !ok {
		return
	}
	// Remove all nodes connected to this device
	r.removeAllNodes(d)
	// Remove from the network topology
	r.graph.RemoveVertex(d)
	// Remove from the device database
	delete(r.devices, id)
}

func (r *Topology) DeviceLinked(ports [2]*Port) {
	link := NewLink(ports)
	if err := r.graph.AddEdge(link); err != nil {
		r.log.Err(fmt.Sprintf("DeviceLinked: %v", err))
		return
	}
}

func (r *Topology) NodeAdded(n *Node) {
	// Write lock
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if n == nil {
		panic("node is nil")
	}

	node, ok := r.nodes[n.MAC().String()]
	// Do we already have a port that has this node?
	if ok {
		// Remove the node from the port
		port := node.Port()
		port.RemoveNode(node.MAC())
	}
	// Add new node
	r.nodes[n.MAC().String()] = n
}

// Node may return nil if a node whose MAC is mac does not exist
func (r *Topology) Node(mac net.HardwareAddr) *Node {
	// Read lock
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	return r.nodes[mac.String()]
}

func (r *Topology) PortRemoved(p *Port) {
	// Write lock
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// Remove hosts connected to the port from the host database
	for _, v := range p.Nodes() {
		delete(r.nodes, v.MAC().String())
		p.RemoveNode(v.MAC())
	}
	// Remove an edge from the graph if this port is an edge connected to another switch
	r.graph.RemoveEdge(p)
}

func (r *Topology) Path(srcDeviceID, dstDeviceID string) [][2]*Port {
	// Read lock
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	v := make([][2]*Port, 0)
	src := r.devices[srcDeviceID]
	dst := r.devices[dstDeviceID]
	// Unknown source or destination device?
	if src == nil || dst == nil {
		// Return empty path
		return v
	}

	path := r.graph.FindPath(src, dst)
	for _, p := range path {
		device := p.V.(*Device)
		link := p.E.(*Link)
		v = append(v, pickPort(device, link))
	}

	return v
}

func pickPort(d *Device, l *Link) [2]*Port {
	p := l.Points()
	if p[0].Vertex().ID() == d.ID() {
		return [2]*Port{p[0].(*Port), p[1].(*Port)}
	}

	return [2]*Port{p[1].(*Port), p[0].(*Port)}
}

func (r *Topology) IsEdge(p *Port) bool {
	return r.graph.IsEdge(p)
}

func (r *Topology) IsEnabledBySTP(p *Port) bool {
	return r.graph.IsEnabledPoint(p)
}