package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

const DEBUG = "\033[31m[*][DEBUG]\033[0m"

const protocolName = "/bitcoin-simulation/1.0.0"

type TopicNetwork struct {
	Messages chan *Message
	Blocks   map[string]string
	Headers  []string

	ctx   context.Context
	ps    *pubsub.PubSub
	topic *pubsub.Topic
	sub   *pubsub.Subscription
	self  peer.ID
}

type Message struct {
	MsgType int //0 data, 1 ihave, 2 iwant
	Sender  peer.ID
	Blocks  map[string]string
	// Header  string
	// Content string
	IHAVE []string
	IWANT []string
}

func JoinNetwork(ctx context.Context, ps *pubsub.PubSub, self peer.ID) (*TopicNetwork, error) {

	//Join the topic
	topic, err := ps.Join(protocolName)
	if err != nil {
		return nil, err
	}

	//Subscribe to the topic
	sub, err := topic.Subscribe()
	if err != nil {
		return nil, err
	}

	net := &TopicNetwork{
		ctx:      ctx,
		ps:       ps,
		topic:    topic,
		sub:      sub,
		self:     self,
		Messages: make(chan *Message, 64),
		Blocks:   make(map[string]string),
		Headers:  make([]string, 0, 128),
	}

	go net.ReadService()
	return net, nil

}

func (net *TopicNetwork) ReadService() {
	for {

		//Get next message in the topic
		received, err := net.sub.Next(net.ctx)
		if err != nil {
			close(net.Messages)
			return
		}

		//If I'm the sender, ignore it
		if received.ReceivedFrom == net.self {
			log.Println("- I am the sender, ignoring the packet")
			continue
		}

		//Get the message
		message := new(Message)
		err = json.Unmarshal(received.Data, message)
		if err != nil {
			continue
		}

		//Debug print to check if the message is parsed correctly
		log.Println("- Received", string(received.Data))

		printMessage(*message)

		//Handle the message
		switch message.MsgType {
		case 0: //Store the data if I don't have the block
			for header, content := range message.Blocks {
				if _, found := net.Blocks[header]; !found {
					net.Blocks[header] = content
					log.Printf("- Stored block %s: %s\n", header, content)
				} else {
					log.Println("- I already have the message", header)
				}
			}
		case 1: //Analyze IHAVE message to see if I need some blocks
			iwant := make([]string, 0, 16)
			for _, owned := range message.IHAVE {
				if _, found := net.Blocks[owned]; !found {
					iwant = append(iwant, owned)
				}
			}
			//If I need some blocks, ask for them with an IWANT message
			if len(iwant) > 0 {
				fmt.Println(DEBUG, fmt.Sprintf("I want these blocks from %s: %s", message.Sender, iwant))
				//Send directly to the peer
			}
			//Forward the IHAVE message in the network to see if someone else needs blocks listed here
			net.Messages <- message
			continue
		case 2: //Analyze IWANT message to see if I can send the messages required
			fmt.Println(DEBUG, fmt.Sprintf("Peer %s wants from me %s", message.Sender, message.IWANT))
			toSend := make(map[string]string)
			for _, wanted := range message.IWANT {
				if block, found := net.Blocks[wanted]; found {
					toSend[wanted] = net.Blocks[block]
					//Messaggio con i blocchi va inviato diretto o in gossip? Cambio il messaggio con una lista di blocchi per
					//		poterne inviare di piu` oppure invio un messaggio per ogni blocco richiesto?
				}
			}
			//TODO:Send the message directly to the peer that requested it
		default:
			log.Fatalf("- Unsupported type of message: %d", message.MsgType)
		}

		// log.Println("Forwarding the message")
		// net.Messages <- message
	}
}

//Publishes the message as the original sender (not forwarding)
func (net *TopicNetwork) Publish(msgType int, blocks map[string]string, ihave []string, iwant []string) error {
	message := &Message{
		MsgType: msgType,
		Sender:  net.self,
		Blocks:  blocks,
		IHAVE:   ihave,
		IWANT:   iwant,
	}
	msg, err := json.Marshal(message)
	if err != nil {
		return err
	}
	log.Println("- Sending", string(msg))
	return net.topic.Publish(net.ctx, msg)
}

//Send directly from a peer to another
// func directSend(sender peer.AddrInfo, receiver peer.AddrInfo, msg Message) error {
// 	//TODO:
// 	return nil
// }

/************ Support Functions ************/

func printMessage(msg Message) {
	switch msg.MsgType {
	case 0:
		log.Printf("- Message DATA received from %s, blocks %s", msg.Sender, msg.Blocks)
	case 1:
		log.Printf("- Message IHAVE received from %s, it has %s", msg.Sender, msg.IHAVE)
	case 2:
		log.Printf("- Message IWANT received from %s, it has %s", msg.Sender, msg.IWANT)
	}
}
