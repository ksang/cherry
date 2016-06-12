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

package openflow

import (
	"encoding"
	"encoding/binary"
)

type Error interface {
	Header
	Class() uint16 // Error type
	Code() uint16
	Data() []byte
	encoding.BinaryUnmarshaler
}

type BaseError struct {
	Message
	class uint16
	code  uint16
	data  []byte
}

func (r *BaseError) Class() uint16 {
	return r.class
}

func (r *BaseError) Code() uint16 {
	return r.code
}

func (r *BaseError) Data() []byte {
	return r.data
}

func (r *BaseError) UnmarshalBinary(data []byte) error {
	if err := r.Message.UnmarshalBinary(data); err != nil {
		return err
	}

	payload := r.Payload()
	if payload == nil || len(payload) < 4 {
		return ErrInvalidPacketLength
	}
	r.class = binary.BigEndian.Uint16(payload[0:2])
	r.code = binary.BigEndian.Uint16(payload[2:4])
	if len(payload) > 4 {
		r.data = payload[4:]
	}

	return nil
}
