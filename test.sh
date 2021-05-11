#!/bin/bash

echo -n "Number of peers to run: "
read numPeer
if [[ ! $numPeer =~ ^[0-9]+$ ]] ; then
    echo "Not an integer"
    exit
fi

echo -n "Number of blocks to be created by each peer: "
read numBlocks
if [[ ! $numBlocks =~ ^[0-9]+$ ]] ; then
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
if command -v go &> /dev/null; then
    go build -o protocol
else
    if test -f protocol; then
        echo "Using executable, Go is not installed"
    else 
        echo "Neither Go nor the executable file are present"
        exit
    fi
fi
rm -rf ./outputTests/
mkdir ./outputTests/
echo "Launching ${numPeer} peers..."
echo

for (( i = 1; i <= ${numPeer}; i++ )); do
	echo "Running peer $i"
	timeout -v --signal=2 ${numMinutes}m ./protocol -blocks ${numBlocks} &> "./outputTests/peer${i}.txt" &
done
