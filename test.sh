#!/bin/bash

echo -n "Use default (30 peers with 20min timeout and 5 blocks each)? [Y/n]: "
read yn
case $yn in

    'Y' | 'y' | '')
        echo "Using default values"
        numPeer=30
        numBlocks=5
        numMinutes=20
        ;;

    'n')
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
        echo -n "Running time (in minutes) for each peer: "
        read numMinutes
        if [[ ! $numMinutes =~ ^[0-9]+$ ]] ; then
            echo "Not an integer"
            exit
        fi
        ;;

    *)
        echo "Unknown option"
        exit
esac

echo -e "\nBuilding code and removing old tests"
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
echo -e "Launching ${numPeer} peers (5 every 3 minutes with ${numMinutes}min timeout)...\n"

#Launch 5 peers every 3 minutes
for (( i = 1; i <= ${numPeer}; i++ )); do
	echo "Running peer #$i"
	timeout -v --signal=2 ${numMinutes}m ./protocol -blocks ${numBlocks} &> "./outputTests/peer${i}.log" &
    sleep 0.5s
    if (( $i == $numPeer )); then
        echo -e "\nFinished"
        exit
    elif (( $i % 5 == 0 )) ; then
        echo -e "\nSleeping for 3 minutes\n"
        sleep 3m
    fi
done
