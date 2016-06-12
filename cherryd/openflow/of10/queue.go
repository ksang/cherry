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

package of10

import (
	"encoding/binary"
	"github.com/superkkt/cherry/cherryd/openflow"
)

type QueueGetConfigRequest struct {
	openflow.Message
	port openflow.OutPort
}

func NewQueueGetConfigRequest(xid uint32) openflow.QueueGetConfigRequest {
	return &QueueGetConfigRequest{
		Message: openflow.NewMessage(openflow.OF10_VERSION, OFPT_QUEUE_GET_CONFIG_REQUEST, xid),
	}
}

func (r *QueueGetConfigRequest) Port() openflow.OutPort {
	return r.port
}

func (r *QueueGetConfigRequest) SetPort(p openflow.OutPort) {
	r.port = p
}

func (r *QueueGetConfigRequest) MarshalBinary() ([]byte, error) {
	v := make([]byte, 4)
	binary.BigEndian.PutUint16(v[0:2], uint16(r.port.Value()))
	// v[2:4] is padding
	r.SetPayload(v)

	return r.Message.MarshalBinary()
}

type Queue struct {
	queueID uint32
	length  uint16
	rate    uint16
}

func (r *Queue) QueueID() uint32 {
	return r.queueID
}

func (r *Queue) Length() uint16 {
	return r.length
}

func (r *Queue) Rate() uint16 {
	return r.rate
}

func (r *Queue) UnmarshalBinary(data []byte) error {
	if len(data) != 24 {
		return openflow.ErrInvalidPacketLength
	}
	r.queueID = binary.BigEndian.Uint32(data[0:4])
	r.length = binary.BigEndian.Uint16(data[4:6])
	// data[6:8] is pad
	property := binary.BigEndian.Uint16(data[8:10])
	if property != 0x01 {
		// Unknown property
		return nil
	}
	r.rate = binary.BigEndian.Uint16(data[16:18])
	return nil
}

func NewQueue() openflow.Queue {
	return &Queue{}
}

type QueueGetConfigReply struct {
	openflow.Message
	port  openflow.OutPort
	queue []openflow.Queue
}

func (r *QueueGetConfigReply) Port() openflow.OutPort {
	return r.port
}

func (r *QueueGetConfigReply) Queue() []openflow.Queue {
	return r.queue
}

func (r *QueueGetConfigReply) UnmarshalBinary(data []byte) error {
	if err := r.Message.UnmarshalBinary(data); err != nil {
		return err
	}
	payload := r.Payload()
	if payload == nil || len(payload) < 8 {
		return openflow.ErrInvalidPacketLength
	}
	r.port.SetValue(uint32(binary.BigEndian.Uint16(payload[0:2])))
	if (len(payload)-8)%24 != 0 {
		return openflow.ErrInvalidPacketLength
	}
	// Unmarshal Queues
	for i := 8; i < len(payload); i += 24 {
		q := NewQueue()
		if err := q.UnmarshalBinary(payload[i : i+24]); err != nil {
			return err
		}
		r.queue = append(r.queue, q)
	}

	return nil
}
