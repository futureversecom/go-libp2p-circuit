package relay

import (
	"errors"
	"fmt"
	"io"

	pb "github.com/futureversecom/go-libp2p-circuit/pb"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"
	"github.com/libp2p/go-libp2p/core/sec/insecure"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/libp2p/go-msgio/protoio"

	"github.com/gogo/protobuf/proto"
	"github.com/multiformats/go-varint"

	"github.com/libp2p/go-libp2p/core/transport"
	swarm "github.com/libp2p/go-libp2p/p2p/net/swarm"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	ma "github.com/multiformats/go-multiaddr"
)

// GenUpgrader creates a new connection upgrader for use with this swarm.
func GenUpgrader(n *swarm.Swarm, connGater connmgr.ConnectionGater, opts ...tptu.Option) (transport.Upgrader, error) {
	id := n.LocalPeer()
	pk := n.Peerstore().PrivKey(id)
	st := insecure.NewWithIdentity(insecure.ID, id, pk)

	u, err := tptu.New([]sec.SecureTransport{st}, []tptu.StreamMuxer{{ID: yamux.ID, Muxer: yamux.DefaultTransport}}, nil, nil, connGater, opts...)
	if err != nil {
		return nil, fmt.Errorf("error creating upgrader: %s", err)
	}

	return u, nil
}

func peerToPeerInfo(p *pb.CircuitRelay_Peer) (peer.AddrInfo, error) {
	if p == nil {
		return peer.AddrInfo{}, errors.New("nil peer")
	}

	id, err := peer.IDFromBytes(p.Id)
	if err != nil {
		return peer.AddrInfo{}, err
	}

	addrs := make([]ma.Multiaddr, 0, len(p.Addrs))
	for _, addrBytes := range p.Addrs {
		a, err := ma.NewMultiaddrBytes(addrBytes)
		if err == nil {
			addrs = append(addrs, a)
		}
	}

	return peer.AddrInfo{ID: id, Addrs: addrs}, nil
}

func peerInfoToPeer(pi peer.AddrInfo) *pb.CircuitRelay_Peer {
	addrs := make([][]byte, len(pi.Addrs))
	for i, addr := range pi.Addrs {
		addrs[i] = addr.Bytes()
	}

	p := new(pb.CircuitRelay_Peer)
	p.Id = []byte(pi.ID)
	p.Addrs = addrs

	return p
}

func incrementTag(v int) int {
	return v + 1
}

func decrementTag(v int) int {
	if v > 0 {
		return v - 1
	} else {
		return v
	}
}

type delimitedReader struct {
	r   io.Reader
	buf []byte
}

// The gogo protobuf NewDelimitedReader is buffered, which may eat up stream data.
// So we need to implement a compatible delimited reader that reads unbuffered.
// There is a slowdown from unbuffered reading: when reading the message
// it can take multiple single byte Reads to read the length and another Read
// to read the message payload.
// However, this is not critical performance degradation as
//   - the reader is utilized to read one (dialer, stop) or two messages (hop) during
//     the handshake, so it's a drop in the water for the connection lifetime.
//   - messages are small (max 4k) and the length fits in a couple of bytes,
//     so overall we have at most three reads per message.
func newDelimitedReader(r io.Reader, maxSize int) *delimitedReader {
	return &delimitedReader{r: r, buf: pool.Get(maxSize)}
}

func (d *delimitedReader) Close() {
	if d.buf != nil {
		pool.Put(d.buf)
		d.buf = nil
	}
}

func (d *delimitedReader) ReadByte() (byte, error) {
	buf := d.buf[:1]
	_, err := d.r.Read(buf)
	return buf[0], err
}

func (d *delimitedReader) ReadMsg(msg proto.Message) error {
	mlen, err := varint.ReadUvarint(d)
	if err != nil {
		return err
	}

	if uint64(len(d.buf)) < mlen {
		return errors.New("message too large")
	}

	buf := d.buf[:mlen]
	_, err = io.ReadFull(d.r, buf)
	if err != nil {
		return err
	}

	return proto.Unmarshal(buf, msg)
}

func newDelimitedWriter(w io.Writer) protoio.WriteCloser {
	return protoio.NewDelimitedWriter(w)
}
