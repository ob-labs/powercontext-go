// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package scheduler

import (
	"encoding/binary"
	"unicode/utf8"
)

// The parser helpers below enforce bounds before every read or stack change.
// Keeping them separate makes the opcode allowlist in pickle.go auditable.
func (p *pickleParser) byte() (byte, error) {
	if p.position >= len(p.data) {
		return 0, invalidState("truncated Pickle")
	}
	value := p.data[p.position]
	p.position++
	return value, nil
}

func (p *pickleParser) uint16() (uint16, error) {
	value, err := p.bytes(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(value), nil
}

func (p *pickleParser) uint32() (uint32, error) {
	value, err := p.bytes(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(value), nil
}

func (p *pickleParser) uint64() (uint64, error) {
	value, err := p.bytes(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(value), nil
}

func (p *pickleParser) bytes(length int) ([]byte, error) {
	return p.bytesLength(uint64(length))
}

func (p *pickleParser) bytesLength(length uint64) ([]byte, error) {
	if length > uint64(len(p.data)-p.position) || length > maxPickleBytes {
		return nil, invalidState("truncated or oversized Pickle value")
	}
	result := append([]byte(nil), p.data[p.position:p.position+int(length)]...)
	p.position += int(length)
	return result, nil
}

func (p *pickleParser) string(length int) (string, error) {
	return p.stringLength(uint64(length))
}

func (p *pickleParser) stringLength(length uint64) (string, error) {
	value, err := p.bytesLength(length)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(value) {
		return "", invalidState("Pickle Unicode value is invalid UTF-8")
	}
	return string(value), nil
}

func (p *pickleParser) long(length int) (int64, error) {
	if length < 1 || length > 8 {
		return 0, invalidState("Pickle integer exceeds int64")
	}
	value, err := p.bytes(length)
	if err != nil {
		return 0, err
	}
	var buffer [8]byte
	fill := byte(0)
	if value[length-1]&0x80 != 0 {
		fill = 0xff
	}
	for index := range buffer {
		buffer[index] = fill
	}
	copy(buffer[:], value)
	return int64(binary.LittleEndian.Uint64(buffer[:])), nil
}

func (p *pickleParser) push(value any) { p.stack = append(p.stack, value) }

func (p *pickleParser) pop() (any, error) {
	if len(p.stack) == 0 {
		return nil, invalidState("Pickle stack underflow")
	}
	index := len(p.stack) - 1
	value := p.stack[index]
	p.stack = p.stack[:index]
	return value, nil
}

func (p *pickleParser) popStringPair() (name, module string, err error) {
	nameValue, err := p.pop()
	if err != nil {
		return "", "", err
	}
	moduleValue, err := p.pop()
	if err != nil {
		return "", "", err
	}
	name, nameOK := nameValue.(string)
	module, moduleOK := moduleValue.(string)
	if !nameOK || !moduleOK {
		return "", "", invalidState("STACK_GLOBAL requires Unicode names")
	}
	return name, module, nil
}

func (p *pickleParser) popTupleGlobal() (pickleTuple, pickleGlobal, error) {
	argsValue, err := p.pop()
	if err != nil {
		return nil, pickleGlobal{}, err
	}
	callableValue, err := p.pop()
	if err != nil {
		return nil, pickleGlobal{}, err
	}
	args, argsOK := argsValue.(pickleTuple)
	callable, callableOK := callableValue.(pickleGlobal)
	if !argsOK || !callableOK {
		return nil, pickleGlobal{}, invalidState("Pickle constructor operands differ")
	}
	return args, callable, nil
}

func (p *pickleParser) fixedTuple(size int) error {
	if len(p.stack) < size {
		return invalidState("Pickle tuple stack underflow")
	}
	start := len(p.stack) - size
	value := append(pickleTuple(nil), p.stack[start:]...)
	p.stack = p.stack[:start]
	p.push(value)
	return nil
}

func (p *pickleParser) markTuple() error {
	if len(p.marks) == 0 {
		return invalidState("Pickle tuple has no mark")
	}
	mark := p.marks[len(p.marks)-1]
	p.marks = p.marks[:len(p.marks)-1]
	if mark > len(p.stack) {
		return invalidState("Pickle tuple mark is outside the stack")
	}
	value := append(pickleTuple(nil), p.stack[mark:]...)
	p.stack = p.stack[:mark]
	p.push(value)
	return nil
}

func (p *pickleParser) setItems() error {
	if len(p.marks) == 0 {
		return invalidState("Pickle SETITEMS has no mark")
	}
	mark := p.marks[len(p.marks)-1]
	p.marks = p.marks[:len(p.marks)-1]
	if mark < 1 || mark > len(p.stack) || (len(p.stack)-mark)%2 != 0 {
		return invalidState("Pickle SETITEMS operands differ")
	}
	target, ok := p.stack[mark-1].(map[string]any)
	if !ok {
		return invalidState("Pickle SETITEMS target is not a dictionary")
	}
	for index := mark; index < len(p.stack); index += 2 {
		key, ok := p.stack[index].(string)
		if !ok {
			return invalidState("Pickle dictionary key is not Unicode")
		}
		if _, exists := target[key]; exists {
			return invalidState("Pickle dictionary contains duplicate keys")
		}
		target[key] = p.stack[index+1]
	}
	p.stack = p.stack[:mark]
	return nil
}

func (p *pickleParser) setItem() error {
	if len(p.stack) < 3 {
		return invalidState("Pickle SETITEM stack underflow")
	}
	value, _ := p.pop()
	keyValue, _ := p.pop()
	target, ok := p.stack[len(p.stack)-1].(map[string]any)
	key, keyOK := keyValue.(string)
	if !ok || !keyOK {
		return invalidState("Pickle SETITEM operands differ")
	}
	if _, exists := target[key]; exists {
		return invalidState("Pickle dictionary contains duplicate keys")
	}
	target[key] = value
	return nil
}

func (p *pickleParser) memoize(index int) error {
	if len(p.stack) == 0 || index < 0 || index >= maxPickleMemo {
		return invalidState("Pickle memo limit or stack violation")
	}
	if index > len(p.memo) {
		return invalidState("Pickle memo index is sparse")
	}
	if index == len(p.memo) {
		p.memo = append(p.memo, p.stack[len(p.stack)-1])
		return nil
	}
	p.memo[index] = p.stack[len(p.stack)-1]
	return nil
}

func (p *pickleParser) getMemo(index int) error {
	if index < 0 || index >= len(p.memo) {
		return invalidState("Pickle memo reference is invalid")
	}
	p.push(p.memo[index])
	return nil
}
