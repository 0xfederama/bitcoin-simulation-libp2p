#!/bin/bash

echo -n "Number of peers to run: "
read numPeer
if [[ ! $numPeer =~ ^[0-9]+$ ]] ; then
    echo "Not an integer"
    exit
fi

echo -n "Running time (in minutes): "
read numMinutes
if [[ ! $numMinutes =~ ^[0-9]+$ ]] ; then
    echo "Not an integer"
    exit
fi

echo
echo "Building and removing old tests"
go build -o protocol
rm -rf ./outputTests/
mkdir ./outputTests/
echo "Launching peers..."
echo

for (( i = 1; i <= ${numPeer}; i++ )); do
	echo "Running peer $i"
	timeout -v --signal=2 ${numMinutes}m ./protocol -blocks 1 &> "./outputTests/peer${i}.txt" &
done
