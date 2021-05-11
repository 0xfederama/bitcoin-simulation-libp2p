package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	ds "github.com/ipfs/go-datastore"
	dsync "github.com/ipfs/go-datastore/sync"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	discovery "github.com/libp2p/go-libp2p-discovery"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	rhost "github.com/libp2p/go-libp2p/p2p/host/routed"
	"github.com/pkg/errors"
)

const protocolTopicName = "/bitcoin-simulation/1.0"

func main() {

	//Parse input flags for the number of blocks created
	numBlocks := flag.Int("blocks", 1, "number of blocks to create")
	flag.Parse()

	//Create the host
	ctx := context.Background()
	host, err := libp2p.New(
		ctx,
		libp2p.Defaults, // /ip4/0.0.0.0/tcp/0, /ip6/::/tcp/0, enable relay, /yamux/1.0.0, /mplex/6.7.0, tls, noise, tcp, ws, empty peerstore
		libp2p.DefaultStaticRelays(),
		libp2p.EnableAutoRelay(),
	)
	if err != nil {
		panic(err)
	}

	fmt.Printf("ID:    %s\nAddrs: %s\n\n", host.ID(), host.Addrs())

	//Make a datastore used by the DHT
	dstore := dsync.MutexWrap(ds.NewMapDatastore())

	//Make the DHT
	kaddht := dht.NewDHT(ctx, host, dstore)

	//Make the routed host
	routedHost := rhost.Wrap(host, kaddht)

	//Bootstrap the DHT
	if err = bootstrapConnect(ctx, routedHost, dht.GetDefaultBootstrapPeerAddrInfos()); err != nil {
		panic(err)
	}
	if err = kaddht.Bootstrap(ctx); err != nil {
		panic(err)
	}

	//Advertise location and find other peers
	routingDiscovery := discovery.NewRoutingDiscovery(kaddht)
	discovery.Advertise(ctx, routingDiscovery, protocolTopicName)
	connectedPeers := make([]peer.ID, 128)
	go func() {
		for {
			peerChan, err := routingDiscovery.FindPeers(ctx, protocolTopicName)
			if err != nil {
				panic(err)
			}
			for p := range peerChan {
				if p.ID == routedHost.ID() || contains(connectedPeers, p.ID) {
					continue
				}
				if len(p.Addrs) > 0 {
					routedHost.Peerstore().AddAddrs(p.ID, p.Addrs, peerstore.ConnectedAddrTTL)
					err := routedHost.Connect(ctx, p)
					if err == nil {
						log.Println("- Connected to", p)
						connectedPeers = append(connectedPeers, p.ID)
					} else {
						log.Printf("- Error connecting to %s: %s\n", p, err)
					}
				}
			}
		}
	}()

	//Create a new PubSub service using GossipSub routing and join the topic
	ps, err := pubsub.NewGossipSub(ctx, routedHost)
	if err != nil {
		panic(err)
	}
	topicNet, err := JoinNetwork(ctx, routedHost, ps, routedHost.ID())
	if err != nil {
		panic(err)
	}

	go periodicSendIHAVE(topicNet)

	//Set stream handler to send and receive direct DATA and IWANT messages using the network struct
	routedHost.SetStreamHandler(protocolTopicName, topicNet.handleStream)

	//Wait for stop signal (Ctrl-C), unsubscribe and close the host
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		topicNet.sub.Cancel()
		err = host.Close()
		if err != nil {
			log.Println(DEBUG, err)
		}
		err = routedHost.Close()
		if err != nil {
			log.Println(DEBUG, err)
		}
		err = kaddht.Close()
		if err != nil {
			log.Println(DEBUG, err)
		}
		fmt.Println("Exiting...")
		os.Exit(0)
	}()

	//Loop to simulate creation of "transactions"
	for i := 0; i < *numBlocks; i++ {
		peers := topicNet.ps.ListPeers(protocolTopicName)
		log.Printf("- Found %d other peers in the network: %s\n", len(peers), peers)
		if PoW() {
			//Create the block
			content := strings.Repeat(strconv.FormatInt(int64(math.Pow(float64(time.Now().Unix()+rand.Int63n(50)), 2)), 16), 8) //Use epoch to create a fake transaction
			hash := md5.Sum([]byte(content))
			header := hex.EncodeToString(hash[:])

			//Store the block and publish it with an IHAVE message
			topicNet.Blocks[header] = content
			topicNet.Headers = append(topicNet.Headers, header)
			log.Printf("- Created block with header: %s and content: %s\n", header, content)
			if err := topicNet.Publish(topicNet.Headers); err != nil {
				log.Println("- Error publishing IHAVE message on the network:", err)
			}
		} else { //If the PoW didn't succeed, retry
			i--
		}
	}

	select {} //Wait forever when finished to create blocks

}

//Bootstrap and connect to the peers
func bootstrapConnect(ctx context.Context, h host.Host, peers []peer.AddrInfo) error {

	//Asynchronously connect to the bootstrap peers
	if len(peers) < 1 {
		return errors.New("not enough bootstrap peers")
	}
	errs := make(chan error, len(peers))
	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Add(1)
		go func(p peer.AddrInfo) {
			defer wg.Done()
			h.Peerstore().AddAddrs(p.ID, p.Addrs, peerstore.ConnectedAddrTTL)
			if err := h.Connect(ctx, p); err != nil {
				log.Printf("- Error connecting to %s:%s\n", p, err)
				errs <- err
				return
			} else {
				log.Println("- Connected to", p)
			}
		}(p)
	}
	wg.Wait()

	//Return errors counting the results
	close(errs)
	count := 0
	var err error
	for err = range errs {
		if err != nil {
			count++
		}
	}
	if count == len(peers) {
		return fmt.Errorf("failed to boostrap: %s", err)
	}
	return nil

}

//Periodically send IHAVE messages on the network for newly entered peers
func periodicSendIHAVE(net *TopicNetwork) {
	for {
		time.Sleep(time.Second * 20)
		peers := net.ps.ListPeers(protocolTopicName)
		log.Printf("- Found %d other peers in the network: %s\n", len(peers), peers)
		if len(net.Headers) > 0 {
			if err := net.Publish(net.Headers); err != nil {
				log.Println("- Error publishing IHAVE message on the network:", err)
			}
		}
	}
}

//Simulate proof of work with probability of creating the block
func PoW() bool {
	time.Sleep(time.Second * 20) //Simulate time used to compute the proof of work
	r := rand.Intn(10) + 1
	if r > 3 { //70% chance of success
		log.Println("- PoW succeeded")
		return true
	} else {
		log.Println("- PoW failed")
		return false
	}
}

//Check if a peer id is in the list given
func contains(list []peer.ID, p peer.ID) bool {
	for _, pid := range list {
		if pid == p {
			return true
		}
	}
	return false
}
