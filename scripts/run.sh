#!/bin/bash

set -e

echo "Building the application..."
go build -o app ./main.go

echo "Starting the application..."
nohup ./app > output.log 2>&1 &

echo "Application ready"