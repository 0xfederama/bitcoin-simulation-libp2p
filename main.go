package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
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

//Used by the stream handler
const protocolName = "/bitcoin-simulation/1.0.0"

func main() {

	//Create the host
	ctx := context.Background()
	host, err := libp2p.New(
		ctx,
		libp2p.Defaults, ///ip4/0.0.0.0/tcp/0, /ip6/::/tcp/0, enable relay, /yamux/1.0.0, /mplex/6.7.0, tls, noise, tcp, ws, empty peerstore
		//libp2p.EnableAutoRelay(), //TODO: enable nat relay
	)
	if err != nil {
		panic(err)
	}
	defer host.Close()

	fmt.Printf("ID:    %s\nAddrs: %s\n\n", host.ID(), host.Addrs())

	//Make a datastore used by the DHT
	dstore := dsync.MutexWrap(ds.NewMapDatastore())

	//Make the DHT
	kaddht := dht.NewDHT(ctx, host, dstore)

	//Make the routed host
	routedhost := rhost.Wrap(host, kaddht)

	//Bootstrap the DHT
	if err = bootstrapConnect(ctx, routedhost, dht.GetDefaultBootstrapPeerAddrInfos()); err != nil {
		panic(err)
	}
	if err = kaddht.Bootstrap(ctx); err != nil {
		panic(err)
	}

	//Advertise location and find other peers
	routingDiscovery := discovery.NewRoutingDiscovery(kaddht)
	discovery.Advertise(ctx, routingDiscovery, protocolName)
	go func() {
		for {
			peerChan, err := routingDiscovery.FindPeers(ctx, protocolName)
			if err != nil {
				panic(err)
			}
			for peer := range peerChan {
				if len(peer.Addrs) > 0 {
					fmt.Println(DEBUG, "Found", peer)
				}
			}
			time.Sleep(time.Second*5)
		}
	}()

	//Create a new PubSub service using GossipSub routing and join the topic
	ps, err := pubsub.NewGossipSub(ctx, routedhost)
	if err != nil {
		panic(err)
	}
	topicNet, err := JoinNetwork(ctx, routedhost, ps, routedhost.ID())
	if err != nil {
		panic(err)
	}

	go periodicSendIHAVE(topicNet)

	//Set stream handler to send and receive direct DATA and IWANT messages using the network struct
	host.SetStreamHandler(protocolName, topicNet.handleStream)
	routedhost.SetStreamHandler(protocolName, topicNet.handleStream)

	//Wait for stop signal (Ctrl-C) and close the host
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		// topicNet.ps.UnregisterTopicValidator(topicName)
		// topicNet.topic.Close()
		// topicNet.sub.Cancel()
		// host.Close()
		// kaddht.Close()
		fmt.Println("Exiting...")
		os.Exit(0)
	}()

	//Loop to simulate creation of "transactions"
	if true {
		for i := 0; i < 2; i++ {
			peers := topicNet.ps.ListPeers(topicName)
			log.Printf("- Found %d other peers in the network: %s\n", len(peers), peers)
			if PoW() {
				//Create the block
				content := strings.Repeat(strconv.FormatInt(int64(math.Pow(float64(time.Now().Unix()), 2)), 16), 8) //Use epoch to create a fake transaction
				hash := md5.Sum([]byte(content))
				header := hex.EncodeToString(hash[:])

				//Store the block and publish it with an IHAVE message
				topicNet.Blocks[header] = content
				topicNet.Headers = append(topicNet.Headers, header)
				log.Printf("- Created block with header %s and content %s\n", header, content)
				if err := topicNet.Publish(topicNet.Headers); err != nil {
					log.Println("- Error publishing IHAVE message on the network:", err)
				}
			}
			time.Sleep(time.Second * 10) //Wait 10 seconds before computing another message
			fmt.Println()
		}
	}

	select {} //FIXME: only for testing peers that do not create blocks

}

//Bootstrap and connect to the peers
func bootstrapConnect(ctx context.Context, h host.Host, peers []peer.AddrInfo) error {
	
	if len(peers) < 1 {
		return errors.New("not enough bootstrap peers")
	}
	errs := make(chan error, len(peers))
	var wg sync.WaitGroup
	for _, p := range peers {
		//peerInfo, _ := peer.AddrInfoFromP2pAddr(peerAddr)
		wg.Add(1)
		go func(p peer.AddrInfo) {
			defer wg.Done()
			h.Peerstore().AddAddrs(p.ID, p.Addrs, peerstore.PermanentAddrTTL)
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
		time.Sleep(time.Second * 15) //FIXME: sleep for 30
		peers := net.ps.ListPeers(topicName)
		log.Printf("- Found %d other peers in the network: %s\n", len(peers), peers)
		if err := net.Publish(net.Headers); err != nil {
			log.Println("- Error publishing IHAVE message on the network:", err)
		}
	}
}

//Simulate proof of work with probability of creating the block
func PoW() bool {
	time.Sleep(time.Second * 10) //Simulate time used to compute the proof of work
	r := rand.Intn(10) + 1
	if r > -1 { //FIXME: >3, testing //70% chance
		log.Println("- PoW succeeded")
		return true
	} else {
		log.Println("- PoW failed")
		return false
	}
}

