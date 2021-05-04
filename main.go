package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/p2p/discovery"
)

//Used by mDNS ads to discover other peers
const discoveryServiceTag = "bitcoin-simulation"

//Notified when a new peer is found via mDNS discovery
type discoveryNotifee struct {
	h   host.Host
	ctx context.Context
}

func main() {

	//Create the host
	ctx := context.Background()
	host, err := libp2p.New(
		ctx,
		libp2p.Defaults, ///ip4/0.0.0.0/tcp/0, /ip6/::/tcp/0, enable relay, /yamux/1.0.0, /mplex/6.7.0, tls, noise, tcp, ws, empty peerstore
		//libp2p.EnableAutoRelay(), //TODO:
	)
	if err != nil {
		panic(err)
	}
	defer host.Close()

	fmt.Printf("ID:    %s\nAddrs: %s\n\n", host.ID(), host.Addrs())

	//Create a new PubSub service using GossipSub routing
	ps, err := pubsub.NewGossipSub(ctx, host)
	if err != nil {
		panic(err)
	}

	//Setup local mDNS discovery
	err = setupDiscovery(ctx, host)
	if err != nil {
		panic(err)
	}

	//Join the topic
	topicNetwork, err := JoinNetwork(ctx, ps, host.ID())
	if err != nil {
		panic(err)
	}

	//Loop to simulate creation of "transactions"
	for {
		peers := topicNetwork.ps.ListPeers(protocolName)
		//log.Println(host.Peerstore().Peers())
		log.Printf("- Found %d peers in the network: %s\n", len(peers), peers)
		if PoW() {
			//Create the block
			content := strings.Repeat(strconv.FormatInt(int64(math.Pow(float64(time.Now().Unix()), 2)), 16), 16) //Use epoch to create a fake transaction
			hash := md5.Sum([]byte(content))
			header := hex.EncodeToString(hash[:])
			//Store the block and publish it with an IHAVE message
			topicNetwork.Blocks[header] = content
			topicNetwork.Headers = append(topicNetwork.Headers, header)
			log.Printf("- Created block with header %s and content %s\n", header, content)
			topicNetwork.Publish(1, map[string]string{}, topicNetwork.Headers, []string{})
		}
		time.Sleep(time.Second * 20) //Wait 20 seconds before computing another message
	}

}

//Create an mDNS discovery service and attach it to the host
func setupDiscovery(ctx context.Context, h host.Host) error {
	disc, err := discovery.NewMdnsService(ctx, h, time.Minute, discoveryServiceTag)
	if err != nil {
		return err
	}
	notifee := discoveryNotifee{h: h, ctx: ctx}
	disc.RegisterNotifee(&notifee)
	return err
}

//Connect to the newly discovered peer
func (notifee *discoveryNotifee) HandlePeerFound(p peer.AddrInfo) {
	if contains(notifee.h.Peerstore().Peers(), p.ID.Pretty()) { //Avoid saving, connecting to and printing the same peer twice
		return
	}
	log.Println("- Discovered peer", p.ID)
	err := notifee.h.Connect(notifee.ctx, p)
	if err != nil {
		log.Printf("- Error connecting to %s: %s", p.ID, err)
	} else {
		log.Println("- Connected to", p.ID)
	}
}

//Simulate proof of work with probability of creating the block
func PoW() bool {
	time.Sleep(time.Second * 3) //Simulate time used to compute the proof of work
	r := rand.Intn(10) + 1
	if r > -1 { //FIXME: >3, testing //70% chance 
		log.Println("- PoW succeeded")
		return true
	} else {
		log.Println("- PoW failed")
		return false
	}
}

func contains(addrs peer.IDSlice, p string) bool {
	for _, addr := range addrs {
		if addr.Pretty() == p {
			return true
		}
	}
	return false
}
