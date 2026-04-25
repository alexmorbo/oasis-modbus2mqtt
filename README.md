# oasis-modbus2mqtt

MQTT bridge for GTC Oasis Syberia v5 ventilation controller via Modbus TCP — exposes Home Assistant MQTT discovery.

## Status

In active development — not production-ready.

## Overview

`oasis-modbus2mqtt` connects to a GTC Oasis Syberia v5 ventilation controller over Modbus TCP, polls its registers, and republishes the state as Home Assistant MQTT discovery entities. Commands published by Home Assistant on the corresponding MQTT topics are translated back into Modbus writes.

## Requirements

- Go 1.24+
- A reachable Modbus TCP endpoint of the Oasis Syberia controller
- An MQTT broker (e.g. Mosquitto) reachable from both this service and Home Assistant

## Build

```bash
make build           # produces ./bin/oasis-modbus2mqtt
make docker-build    # produces oasis-modbus2mqtt:latest
```

## Development

```bash
make test            # unit tests (short mode)
make lint            # golangci-lint
make tidy            # go mod tidy
```

## License

MIT
