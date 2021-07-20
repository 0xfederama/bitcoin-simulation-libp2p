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

	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ip2location/ip2location-go"
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

const protocolTopicName = "/bitcoin-simulation/1.0.6"
const timePoW = 30       //Time to simulate PoW
const probPoW = 7        //Probability out of 10
const contentRepeat = 8  //Repetitions of content in block, every repetition is 16 chars (128 bytes)
const periodicIHAVE = 15 //Time between two IHAVE messages
const peerLocate = true  //Enable/Disable peer localitazion

func main() {

	//Set logger to print microseconds and parse input flags
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	numBlocks := flag.Int("blocks", 1, "number of blocks to create")
	flag.Parse()

	log.Println("- Protocol started")

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

	fmt.Printf("\nID:    %s\nAddrs: %s\n\n", host.ID(), host.Addrs())

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

	//Advertise location
	routingDiscovery := discovery.NewRoutingDiscovery(kaddht)
	discovery.Advertise(ctx, routingDiscovery, protocolTopicName)
	connectedPeers := make([]peer.ID, 128)
	//Find peers in loop and store them in the peerstore
	go func() {
		for {
			peerChan, err := routingDiscovery.FindPeers(ctx, protocolTopicName)
			if err != nil {
				panic(err)
			}
			for p := range peerChan {
				if p.ID == routedHost.ID() || containsID(connectedPeers, p.ID) {
					continue
				}
				//Store addresses in the peerstore and connect to the peer found (and localize it)
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

	//Periodically send IHAVE messages on the network and locate IPs in the routing table
	go locateIPAddr(kaddht, routedHost)
	go periodicSendIHAVE(topicNet)

	//Set stream handler to send and receive direct DATA and IWANT messages using the network struct
	routedHost.SetStreamHandler(protocolTopicName, topicNet.handleStream)

	//Wait for stop signal (Ctrl-C), unsubscribe and close the host
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	go func() {
		<-stop
		for _, conn := range routedHost.Network().Conns() {
			for _, stream := range conn.GetStreams() {
				stream.Reset()
				stream.CloseRead()
				stream.CloseWrite()
			}
		}
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
			content := strings.Repeat(strconv.FormatInt(int64(math.Pow(float64(time.Now().Unix()-rand.Int63n(1000000)), 2)), 16), contentRepeat) //Use epoch to create a fake transaction
			hash := md5.Sum([]byte(content))
			header := hex.EncodeToString(hash[:])

			//Store the block and publish it with an IHAVE message
			topicNet.Blocks[header] = content
			topicNet.Headers = append(topicNet.Headers, header)
			log.Printf("- Created block #%d with header: %s and content (x %d): %s\n", len(topicNet.Headers), header, contentRepeat, content[:16])
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
		time.Sleep(time.Second * periodicIHAVE)
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
	time.Sleep(time.Second * timePoW) //Simulate time used to compute the proof of work
	r := rand.Intn(10) + 1
	if r <= probPoW {
		log.Println("- PoW succeeded")
		return true
	} else {
		log.Println("- PoW failed")
		return false
	}
}

//Locate ip addresses stored in the peerstore in loop
func locateIPAddr(kaddht *dht.IpfsDHT, host *rhost.RoutedHost) {

	if !peerLocate {
		log.Println("- Peer localitazion disabled")
		return
	}

	//Open the ip database
	db, err := ip2location.OpenDB("./IP2LOCATION-LITE-DB3.BIN")
	if err != nil {
		log.Println("- Error opening ip database:", err)
		log.Println("- Skipping peer localization")
		return
	}
	defer db.Close()

	for {
		//For each peer in the routing table, find its address in the peerstore and locate it
		for _, p := range kaddht.RoutingTable().ListPeers() {
			listip := make([]string, 0)
			//Analyze every address of the peer found
			for _, addr := range host.Peerstore().PeerInfo(p).Addrs {
				//Locate only new ip4 addresses
				if addr.Protocols()[0].Name == "ip4" {
					ip := strings.Split(addr.String(), "/")[2]
					//Ignore loopback address
					if ip != "127.0.0.1" {
						if !containsIP(listip, ip) {
							listip = append(listip, ip)
							res, err := db.Get_all(ip)
							if err != nil {
								log.Println("- Error searching for ip:", err)
							} else {
								//If a LAN ip is found, ignore it
								if res.Country_long != "-" {
									//Search also for Organization Name and AS (if present)
									cmd := "whois " + ip + " | awk '/OriginAS:/{$1 = \"\"; AS=$0; next} /OrgName:/{$1 = \"\"; print $0\" -\"AS}'"
									out, _ := exec.Command("bash", "-c", cmd).CombinedOutput()
									outTrim := strings.Replace(strings.TrimSpace(string(out)), "\n", " ,", -1)
									log.Printf("- Found %s in %s, %s, %s by (%s)\n", ip, res.Country_long, res.Region, res.City, outTrim)
								}
							}
						}

					}
				}
			}
		}
		time.Sleep(time.Second * 120)
	}

}

//Check if a string is already in
func containsIP(list []string, ip string) bool {
	for _, found := range list {
		if found == ip {
			return true
		}
	}
	return false
}

//Check if a peer id is in the list given
func containsID(list []peer.ID, p peer.ID) bool {
	for _, pid := range list {
		if pid == p {
			return true
		}
	}
	return false
}
