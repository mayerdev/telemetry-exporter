#!/bin/bash

APP_NAME="telemetry-exporter"
GOOS=linux GOARCH=amd64 go build -o ${APP_NAME}-linux-x64 ./cmd/exporter/main.go
