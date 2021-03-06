/*
 * Cherry - An OpenFlow Controller
 *
 * Copyright (C) 2015 Samjung Data Service, Inc. All rights reserved.
 * Kitae Kim <superkkt@sds.co.kr>
 *
 * This program is free software; you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation; either version 2 of the License, or
 * any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License along
 * with this program; if not, write to the Free Software Foundation, Inc.,
 * 51 Franklin Street, Fifth Floor, Boston, MA 02110-1301 USA.
 */

package network

import (
	"bytes"
	"encoding"
	"errors"
	"fmt"
	"github.com/superkkt/cherry/cherryd/log"
	"github.com/superkkt/cherry/cherryd/openflow"
	"github.com/superkkt/cherry/cherryd/openflow/of10"
	"github.com/superkkt/cherry/cherryd/openflow/of13"
	"github.com/superkkt/cherry/cherryd/openflow/trans"
	"github.com/superkkt/cherry/cherryd/protocol"
	"golang.org/x/net/context"
	"io"
	"net"
	"strconv"
)

var (
	errNotNegotiated = errors.New("invalid command on non-negotiated session")
)

type session struct {
	negotiated bool
	log        log.Logger
	device     *Device
	trans      *trans.Transceiver
	handler    trans.Handler
	watcher    watcher
	finder     Finder
	listener   ControllerEventListener
	// A cancel function to disconnect this session.
	canceller context.CancelFunc
}

type sessionConfig struct {
	conn     net.Conn
	logger   log.Logger
	watcher  watcher
	finder   Finder
	listener ControllerEventListener
}

func checkParam(c sessionConfig) {
	if c.conn == nil {
		panic("Conn is nil")
	}
	if c.logger == nil {
		panic("Logger is nil")
	}
	if c.watcher == nil {
		panic("Watcher is nil")
	}
	if c.finder == nil {
		panic("Finder is nil")
	}
	if c.listener == nil {
		panic("Listener is nil")
	}
}

func newSession(c sessionConfig) *session {
	checkParam(c)

	stream := trans.NewStream(c.conn)
	v := new(session)
	v.log = c.logger
	v.watcher = c.watcher
	v.finder = c.finder
	v.listener = c.listener
	v.device = newDevice(c.logger, v)
	v.trans = trans.NewTransceiver(stream, v)

	return v
}

func (r *session) OnHello(f openflow.Factory, w trans.Writer, v openflow.Hello) error {
	r.log.Debug(fmt.Sprintf("Session: HELLO (ver=%v) is received", v.Version()))

	// Ignore duplicated HELLO messages
	if r.negotiated {
		return nil
	}

	switch v.Version() {
	case openflow.OF10_VERSION:
		r.handler = newOF10Session(r.log, r.device)
	case openflow.OF13_VERSION:
		r.handler = newOF13Session(r.log, r.device)
	default:
		return fmt.Errorf("unsupported OpenFlow version: %v", v.Version())
	}
	r.device.setFactory(f)
	r.negotiated = true

	return r.handler.OnHello(f, w, v)
}

func (r *session) OnError(f openflow.Factory, w trans.Writer, v openflow.Error) error {
	// Is this the CHECK_OVERLAP error?
	if v.Class() == 3 && v.Code() == 1 {
		// Ignore this CHECK_OVERLAP error
		r.log.Debug("Session: FLOW_MOD is overlapped")
		return nil
	}

	r.log.Err(fmt.Sprintf("Session: ERROR (class=%v, code=%v, data=%v)", v.Class(), v.Code(), v.Data()))
	if !r.negotiated {
		return errNotNegotiated
	}

	return r.handler.OnError(f, w, v)
}

func (r *session) OnFeaturesReply(f openflow.Factory, w trans.Writer, v openflow.FeaturesReply) error {
	r.log.Debug(fmt.Sprintf("Session: FEATURES_REPLY (DPID=%v, NumBufs=%v, NumTables=%v)", v.DPID(), v.NumBuffers(), v.NumTables()))

	if !r.negotiated {
		return errNotNegotiated
	}

	dpid := strconv.FormatUint(v.DPID(), 10)
	// Already connected device?
	if r.finder.Device(dpid) != nil {
		cancel, ok := popCanceller(dpid)
		if ok {
			// Disconnect the previous session. Sometimes, the Dell switch tries to
			// make a new fresh connection even if it already has a main connection.
			// I guess this occurrs when there is a momentary abnormal physical
			// disconnection between the switch and Cherry. After that, the switch
			// does not work properly, so we have to disconnect the previous session
			// so that Cherry allows a new fresh connection.
			cancel()
		}
		return errors.New("duplicated device DPID (aux. connection is not supported yet)")
	}
	r.device.setID(dpid)
	pushCanceller(dpid, r.canceller)
	// We assume a device is up after setting its DPID
	if err := r.listener.OnDeviceUp(r.finder, r.device); err != nil {
		return err
	}
	r.watcher.DeviceAdded(r.device)

	features := Features{
		DPID:       v.DPID(),
		NumBuffers: v.NumBuffers(),
		NumTables:  v.NumTables(),
	}
	r.device.setFeatures(features)

	return r.handler.OnFeaturesReply(f, w, v)
}

func (r *session) OnGetConfigReply(f openflow.Factory, w trans.Writer, v openflow.GetConfigReply) error {
	r.log.Debug("Session: GET_CONFIG_REPLY is received")

	if !r.negotiated {
		return errNotNegotiated
	}

	return r.handler.OnGetConfigReply(f, w, v)
}

func (r *session) OnDescReply(f openflow.Factory, w trans.Writer, v openflow.DescReply) error {
	r.log.Debug("Session: DESC_REPLY is received")

	if !r.negotiated {
		return errNotNegotiated
	}

	r.log.Debug(fmt.Sprintf("Session: Manufacturer=%v", v.Manufacturer()))
	r.log.Debug(fmt.Sprintf("Session: Hardware=%v", v.Hardware()))
	r.log.Debug(fmt.Sprintf("Session: Software=%v", v.Software()))
	r.log.Debug(fmt.Sprintf("Session: Serial=%v", v.Serial()))
	r.log.Debug(fmt.Sprintf("Session: Description=%v", v.Description()))

	desc := Descriptions{
		Manufacturer: v.Manufacturer(),
		Hardware:     v.Hardware(),
		Software:     v.Software(),
		Serial:       v.Serial(),
		Description:  v.Description(),
	}
	r.device.setDescriptions(desc)

	return r.handler.OnDescReply(f, w, v)
}

func (r *session) OnPortDescReply(f openflow.Factory, w trans.Writer, v openflow.PortDescReply) error {
	r.log.Debug(fmt.Sprintf("Session: PORT_DESC_REPLY is received (# of ports=%v)", len(v.Ports())))

	if !r.negotiated {
		return errNotNegotiated
	}

	return r.handler.OnPortDescReply(f, w, v)
}

func newLLDPEtherFrame(deviceID string, port openflow.Port) ([]byte, error) {
	lldp := &protocol.LLDP{
		ChassisID: protocol.LLDPChassisID{
			SubType: 7, // Locally assigned alpha-numeric string
			Data:    []byte(deviceID),
		},
		PortID: protocol.LLDPPortID{
			SubType: 5, // Interface Name
			Data:    []byte(fmt.Sprintf("cherry/%v", port.Number())),
		},
		TTL: 120,
	}
	payload, err := lldp.MarshalBinary()
	if err != nil {
		return nil, err
	}

	ethernet := &protocol.Ethernet{
		SrcMAC: port.MAC(),
		// LLDP multicast MAC address
		DstMAC: []byte{0x01, 0x80, 0xC2, 0x00, 0x00, 0x0E},
		// LLDP ethertype
		Type:    0x88CC,
		Payload: payload,
	}
	frame, err := ethernet.MarshalBinary()
	if err != nil {
		return nil, err
	}

	return frame, nil
}

func sendLLDP(deviceID string, f openflow.Factory, w trans.Writer, p openflow.Port) error {
	lldp, err := newLLDPEtherFrame(deviceID, p)
	if err != nil {
		return err
	}

	outPort := openflow.NewOutPort()
	outPort.SetValue(p.Number())

	// Packet out to the port
	action, err := f.NewAction()
	if err != nil {
		return err
	}
	action.SetOutPort(outPort)

	out, err := f.NewPacketOut()
	if err != nil {
		return err
	}
	// From controller
	out.SetInPort(openflow.NewInPort())
	out.SetAction(action)
	out.SetData(lldp)

	return w.Write(out)
}

func (r *session) sendPortEvent(portNum uint32, up bool) {
	port := r.device.Port(portNum)
	if port == nil {
		return
	}

	if up {
		if err := r.listener.OnPortUp(r.finder, port); err != nil {
			r.log.Err(fmt.Sprintf("Session: OnPortUp: %v", err))
			return
		}
	} else {
		if err := r.listener.OnPortDown(r.finder, port); err != nil {
			r.log.Err(fmt.Sprintf("Session: OnPortDown: %v", err))
			return
		}
	}
}

func (r *session) updatePort(v openflow.PortStatus) {
	port := v.Port()

	switch v.Version() {
	case openflow.OF10_VERSION:
		if port.Number() > of10.OFPP_MAX {
			return
		}
	case openflow.OF13_VERSION:
		if port.Number() > of13.OFPP_MAX {
			return
		}
	default:
		panic("unsupported OpenFlow version")
	}
	r.device.updatePort(port.Number(), port)
}

func (r *session) OnPortStatus(f openflow.Factory, w trans.Writer, v openflow.PortStatus) error {
	r.log.Debug("Session: PORT_STATUS is received")

	if !r.negotiated {
		return errNotNegotiated
	}

	port := v.Port()
	r.log.Debug(fmt.Sprintf("Session: Device=%v, PortNum=%v, AdminUp=%v, LinkUp=%v", r.device.ID(), port.Number(), !port.IsPortDown(), !port.IsLinkDown()))
	r.updatePort(v)

	// Send port event
	up := !port.IsPortDown() && !port.IsLinkDown()
	r.sendPortEvent(port.Number(), up)

	// Is this an enabled port?
	if up && r.device.isValid() {
		// Send LLDP to update network topology
		if err := sendLLDP(r.device.ID(), f, w, port); err != nil {
			return err
		}
	} else {
		// Send port removed event
		p := r.device.Port(port.Number())
		if p != nil {
			r.watcher.PortRemoved(p)
		}
	}

	return r.handler.OnPortStatus(f, w, v)
}

func (r *session) OnFlowRemoved(f openflow.Factory, w trans.Writer, v openflow.FlowRemoved) error {
	r.log.Debug(fmt.Sprintf("Session: FLOW_REMOVED is received (cookie=%v)", v.Cookie()))

	if !r.negotiated {
		return errNotNegotiated
	}

	return r.handler.OnFlowRemoved(f, w, v)
}

func getEthernet(packet []byte) (*protocol.Ethernet, error) {
	eth := new(protocol.Ethernet)
	if err := eth.UnmarshalBinary(packet); err != nil {
		return nil, err
	}

	return eth, nil
}

func isLLDP(e *protocol.Ethernet) bool {
	return e.Type == 0x88CC
}

func getLLDP(packet []byte) (*protocol.LLDP, error) {
	lldp := new(protocol.LLDP)
	if err := lldp.UnmarshalBinary(packet); err != nil {
		return nil, err
	}

	return lldp, nil
}

func isCherryLLDP(p *protocol.LLDP) bool {
	// We sent a LLDP packet that has ChassisID.SubType=7, PortID.SubType=5,
	// and port ID starting with "cherry/".
	if p.ChassisID.SubType != 7 || p.ChassisID.Data == nil {
		// Do nothing if this packet is not the one we sent
		return false
	}
	if p.PortID.SubType != 5 || p.PortID.Data == nil {
		return false
	}
	if len(p.PortID.Data) <= 7 || !bytes.HasPrefix(p.PortID.Data, []byte("cherry/")) {
		return false
	}

	return true
}

func extractDeviceInfo(p *protocol.LLDP) (deviceID string, portNum uint32, err error) {
	if !isCherryLLDP(p) {
		return "", 0, errors.New("not found cherry LLDP packet")
	}

	deviceID = string(p.ChassisID.Data)
	// PortID.Data string consists of "cherry/" and port number
	num, err := strconv.ParseUint(string(p.PortID.Data[7:]), 10, 32)
	if err != nil {
		return "", 0, err
	}

	return deviceID, uint32(num), nil
}

func (r *session) findNeighborPort(deviceID string, portNum uint32) (*Port, error) {
	device := r.finder.Device(deviceID)
	if device == nil {
		return nil, fmt.Errorf("failed to find a neighbor device: deviceID=%v", deviceID)
	}
	port := device.Port(portNum)
	if port == nil {
		return nil, fmt.Errorf("failed to find a neighbor port: deviceID=%v, portNum=%v", deviceID, portNum)
	}

	return port, nil
}

func (r *session) handleLLDP(inPort *Port, ethernet *protocol.Ethernet) error {
	lldp, err := getLLDP(ethernet.Payload)
	if err != nil {
		return err
	}
	deviceID, portNum, err := extractDeviceInfo(lldp)
	if err != nil {
		// Do nothing if this packet is not the one we sent
		r.log.Info("Session: ignoring a LLDP packet issued by an unknown device")
		return nil
	}
	port, err := r.findNeighborPort(deviceID, portNum)
	if err != nil {
		// Do nothing if we cannot find neighbor device and its port
		r.log.Warning(fmt.Sprintf("Session: ignoring a LLDP packet: %v", err))
		return nil
	}
	r.watcher.DeviceLinked([2]*Port{inPort, port})

	return nil
}

func (r *session) isActivatedPort(p *Port) bool {
	// We assume that a port is in inactive state during specified time after setting its value to avoid broadcast storm.
	return p.duration().Seconds() > 1.5
}

func (r *session) OnPacketIn(f openflow.Factory, w trans.Writer, v openflow.PacketIn) error {
	if !r.negotiated {
		return errNotNegotiated
	}
	r.log.Debug(fmt.Sprintf("Session: PACKET_IN is received (device=%v, inport=%v, reason=%v, tableID=%v, cookie=%v)", r.device.ID(), v.InPort(), v.Reason(), v.TableID(), v.Cookie()))

	ethernet, err := getEthernet(v.Data())
	if err != nil {
		return err
	}
	inPort := r.device.Port(v.InPort())
	if inPort == nil {
		r.log.Err(fmt.Sprintf("Session: failed to find a port: deviceID=%v, portNum=%v, so ignore PACKET_IN..", r.device.ID(), v.InPort()))
		return nil
	}
	// Process LLDP, and then add an edge among two switches
	if isLLDP(ethernet) {
		return r.handleLLDP(inPort, ethernet)
	}
	// Do nothing if the ingress port is in inactive state
	if !r.isActivatedPort(inPort) {
		r.log.Debug(fmt.Sprintf("Session: ignoring PACKET_IN from %v:%v because the ingress port is not in active state yet", r.device.ID(), v.InPort()))
		return nil
	}
	// Do nothing if the ingress port is an edge between switches and is disabled by STP.
	if r.finder.IsEdge(inPort) && !r.finder.IsEnabledBySTP(inPort) {
		r.log.Debug(fmt.Sprintf("Session: ignoring PACKET_IN from %v:%v by STP", r.device.ID(), v.InPort()))
		return nil
	}
	// Call specific version handler
	if err := r.handler.OnPacketIn(f, w, v); err != nil {
		return err
	}

	return r.listener.OnPacketIn(r.finder, inPort, ethernet)
}

func (r *session) Run(ctx context.Context) {
	sessionCtx, canceller := context.WithCancel(ctx)
	// This canceller will be used to disconnect this session when it is necessary.
	r.canceller = canceller
	if err := r.trans.Run(sessionCtx); err != nil && err != io.EOF {
		r.log.Err(fmt.Sprintf("Session: transceiver is closed: %v", err))
	}
	r.trans.Close()
	r.device.Close()
	r.log.Debug(fmt.Sprintf("Session: disconnected device (DPID=%v)", r.device.ID()))

	if r.device.isValid() {
		popCanceller(r.device.ID())
		if err := r.listener.OnDeviceDown(r.finder, r.device); err != nil {
			r.log.Err(fmt.Sprintf("Session: executing OnDeviceDown: %v", err))
		}
		r.watcher.DeviceRemoved(r.device)
	}
}

func (r *session) Write(msg encoding.BinaryMarshaler) error {
	return r.trans.Write(msg)
}

func sendHello(f openflow.Factory, w trans.Writer) error {
	msg, err := f.NewHello()
	if err != nil {
		return err
	}

	return w.Write(msg)
}

func sendSetConfig(f openflow.Factory, w trans.Writer) error {
	msg, err := f.NewSetConfig()
	if err != nil {
		return err
	}
	msg.SetFlags(openflow.FragNormal)
	msg.SetMissSendLength(0xFFFF)

	return w.Write(msg)
}

func sendFeaturesRequest(f openflow.Factory, w trans.Writer) error {
	msg, err := f.NewFeaturesRequest()
	if err != nil {
		return err
	}

	return w.Write(msg)
}

func sendDescriptionRequest(f openflow.Factory, w trans.Writer) error {
	msg, err := f.NewDescRequest()
	if err != nil {
		return err
	}

	return w.Write(msg)
}

func sendBarrierRequest(f openflow.Factory, w trans.Writer) error {
	msg, err := f.NewBarrierRequest()
	if err != nil {
		return err
	}

	return w.Write(msg)
}

func sendPortDescriptionRequest(f openflow.Factory, w trans.Writer) error {
	msg, err := f.NewPortDescRequest()
	if err != nil {
		return err
	}

	return w.Write(msg)
}

func setARPSender(f openflow.Factory, w trans.Writer) error {
	match, err := f.NewMatch()
	if err != nil {
		return err
	}
	match.SetEtherType(0x0806) // ARP

	outPort := openflow.NewOutPort()
	outPort.SetController()

	action, err := f.NewAction()
	if err != nil {
		return err
	}
	action.SetOutPort(outPort)
	inst, err := f.NewInstruction()
	if err != nil {
		return err
	}
	inst.ApplyAction(action)

	flow, err := f.NewFlowMod(openflow.FlowAdd)
	if err != nil {
		return err
	}
	// Permanent flow
	flow.SetIdleTimeout(0)
	flow.SetHardTimeout(0)
	flow.SetPriority(100)
	flow.SetFlowMatch(match)
	flow.SetFlowInstruction(inst)

	if err := w.Write(flow); err != nil {
		return err
	}

	return sendBarrierRequest(f, w)
}

func sendRemovingAllFlows(f openflow.Factory, w trans.Writer) error {
	match, err := f.NewMatch() // Wildcard
	if err != nil {
		return err
	}

	msg, err := f.NewFlowMod(openflow.FlowDelete)
	if err != nil {
		return err
	}
	// Wildcard
	msg.SetTableID(0xFF)
	msg.SetFlowMatch(match)

	return w.Write(msg)
}

func sendQueueConfigRequest(f openflow.Factory, w trans.Writer, port uint32) error {
	msg, err := f.NewQueueGetConfigRequest()
	if err != nil {
		return err
	}
	p := openflow.NewOutPort()
	p.SetValue(port)
	msg.SetPort(p)

	return w.Write(msg)
}
