// Package dto defines the data transfer objects exchanged between the
// interface layer (MQTT subscriber, HTTP handlers) and the application
// layer (use cases), plus the sentinel errors use cases can return.
//
// DTOs depend on domain/valueobject for value types but never on
// domain/entity — entity types are returned directly by use cases when
// state needs to flow back out.
package dto

import "errors"

// ErrModbusNotConnected is returned by use cases when ModbusClient.Connected()
// reports false at the moment of dispatch.
var ErrModbusNotConnected = errors.New("modbus client not connected")

// ErrMQTTNotConnected is returned by use cases when MQTTPublisher.Connected()
// reports false at the moment of publish.
var ErrMQTTNotConnected = errors.New("mqtt publisher not connected")

// ErrTransitionInProgress is returned by ApplyCommandUseCase when the latest
// snapshot reports the controller is in a transitional operation (start-up,
// shut-down, defrost) and a new command would conflict.
var ErrTransitionInProgress = errors.New("controller transition in progress")

// ErrCommandTimeout is returned when a queued command is not picked up by
// the dispatcher within its deadline.
var ErrCommandTimeout = errors.New("command timeout")

// ErrInvalidPayload is returned by CommandSubscriber when an MQTT payload
// cannot be decoded into a known Command.
var ErrInvalidPayload = errors.New("invalid command payload")
