package quic

import (
	"bytes"
	"errors"

	"github.com/golang/mock/gomock"
	"github.com/lucas-clemente/quic-go/internal/mocks"
	"github.com/lucas-clemente/quic-go/internal/protocol"
	"github.com/lucas-clemente/quic-go/internal/qerr"
	"github.com/lucas-clemente/quic-go/internal/wire"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Packet Unpacker", func() {
	const version = protocol.VersionTLS
	var (
		unpacker *packetUnpacker
		cs       *mocks.MockCryptoSetup
		connID   = protocol.ConnectionID{0xde, 0xad, 0xbe, 0xef}
	)

	getHeader := func(extHdr *wire.ExtendedHeader) (*wire.Header, []byte) {
		buf := &bytes.Buffer{}
		Expect(extHdr.Write(buf, protocol.VersionWhatever)).To(Succeed())
		hdr, err := wire.ParseHeader(bytes.NewReader(buf.Bytes()), connID.Len())
		Expect(err).ToNot(HaveOccurred())
		return hdr, buf.Bytes()
	}

	BeforeEach(func() {
		cs = mocks.NewMockCryptoSetup(mockCtrl)
		unpacker = newPacketUnpacker(cs, version).(*packetUnpacker)
	})

	It("errors if the packet doesn't contain any payload", func() {
		extHdr := &wire.ExtendedHeader{
			Header:          wire.Header{DestConnectionID: connID},
			PacketNumber:    42,
			PacketNumberLen: protocol.PacketNumberLen2,
		}
		hdr, hdrRaw := getHeader(extHdr)
		data := append(hdrRaw, []byte("foobar")...) // add some payload
		// return an empty (unencrypted) payload
		opener := mocks.NewMockOpener(mockCtrl)
		cs.EXPECT().GetOpener(protocol.Encryption1RTT).Return(opener, nil)
		opener.EXPECT().DecryptHeader(gomock.Any(), gomock.Any(), gomock.Any())
		opener.EXPECT().Open(gomock.Any(), []byte("foobar"), extHdr.PacketNumber, hdrRaw).Return([]byte{}, nil)
		_, err := unpacker.Unpack(hdr, data)
		Expect(err).To(MatchError(qerr.MissingPayload))
	})

	It("opens Initial packets", func() {
		extHdr := &wire.ExtendedHeader{
			Header: wire.Header{
				IsLongHeader:     true,
				Type:             protocol.PacketTypeInitial,
				Length:           3 + 6, // packet number len + payload
				DestConnectionID: connID,
				Version:          version,
			},
			PacketNumber:    2,
			PacketNumberLen: 3,
		}
		hdr, hdrRaw := getHeader(extHdr)
		opener := mocks.NewMockOpener(mockCtrl)
		cs.EXPECT().GetOpener(protocol.EncryptionInitial).Return(opener, nil)
		opener.EXPECT().DecryptHeader(gomock.Any(), gomock.Any(), gomock.Any())
		opener.EXPECT().Open(gomock.Any(), []byte("foobar"), extHdr.PacketNumber, hdrRaw).Return([]byte{0}, nil)
		packet, err := unpacker.Unpack(hdr, append(hdrRaw, []byte("foobar")...))
		Expect(err).ToNot(HaveOccurred())
		Expect(packet.encryptionLevel).To(Equal(protocol.EncryptionInitial))
	})

	It("errors on packets that are smaller than the length in the packet header", func() {
		extHdr := &wire.ExtendedHeader{
			Header: wire.Header{
				IsLongHeader:     true,
				Type:             protocol.PacketTypeHandshake,
				Length:           1000,
				DestConnectionID: connID,
				Version:          version,
			},
			PacketNumberLen: protocol.PacketNumberLen2,
		}
		hdr, hdrRaw := getHeader(extHdr)
		data := append(hdrRaw, make([]byte, 500-2 /* for packet number length */)...)
		_, err := unpacker.Unpack(hdr, data)
		Expect(err).To(MatchError("packet length (500 bytes) is smaller than the expected length (1000 bytes)"))
	})

	It("cuts packets to the right length", func() {
		pnLen := protocol.PacketNumberLen2
		extHdr := &wire.ExtendedHeader{
			Header: wire.Header{
				IsLongHeader:     true,
				DestConnectionID: connID,
				Type:             protocol.PacketTypeHandshake,
				Length:           456,
				Version:          protocol.VersionTLS,
			},
			PacketNumberLen: pnLen,
		}
		payloadLen := 456 - int(pnLen)
		hdr, hdrRaw := getHeader(extHdr)
		data := append(hdrRaw, make([]byte, payloadLen)...)
		opener := mocks.NewMockOpener(mockCtrl)
		cs.EXPECT().GetOpener(protocol.EncryptionHandshake).Return(opener, nil)
		opener.EXPECT().DecryptHeader(gomock.Any(), gomock.Any(), gomock.Any())
		opener.EXPECT().Open(gomock.Any(), gomock.Any(), extHdr.PacketNumber, hdrRaw).DoAndReturn(func(_, payload []byte, _ protocol.PacketNumber, _ []byte) ([]byte, error) {
			Expect(payload).To(HaveLen(payloadLen))
			return []byte{0}, nil
		})
		_, err := unpacker.Unpack(hdr, data)
		Expect(err).ToNot(HaveOccurred())
	})

	It("returns the error when getting the sealer fails", func() {
		extHdr := &wire.ExtendedHeader{
			Header:          wire.Header{DestConnectionID: connID},
			PacketNumber:    0x1337,
			PacketNumberLen: 2,
		}
		hdr, hdrRaw := getHeader(extHdr)
		cs.EXPECT().GetOpener(protocol.Encryption1RTT).Return(nil, errors.New("test err"))
		_, err := unpacker.Unpack(hdr, hdrRaw)
		Expect(err).To(MatchError(qerr.Error(qerr.DecryptionFailure, "test err")))
	})

	It("returns the error when unpacking fails", func() {
		extHdr := &wire.ExtendedHeader{
			Header: wire.Header{
				IsLongHeader:     true,
				Type:             protocol.PacketTypeHandshake,
				Length:           3, // packet number len
				DestConnectionID: connID,
				Version:          version,
			},
			PacketNumber:    2,
			PacketNumberLen: 3,
		}
		hdr, hdrRaw := getHeader(extHdr)
		opener := mocks.NewMockOpener(mockCtrl)
		cs.EXPECT().GetOpener(protocol.EncryptionHandshake).Return(opener, nil)
		opener.EXPECT().DecryptHeader(gomock.Any(), gomock.Any(), gomock.Any())
		opener.EXPECT().Open(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errors.New("test err"))
		_, err := unpacker.Unpack(hdr, hdrRaw)
		Expect(err).To(MatchError(qerr.Error(qerr.DecryptionFailure, "test err")))
	})

	It("decrypts the header", func() {
		extHdr := &wire.ExtendedHeader{
			Header: wire.Header{
				IsLongHeader:     true,
				Type:             protocol.PacketTypeHandshake,
				Length:           3, // packet number len
				DestConnectionID: connID,
				Version:          version,
			},
			PacketNumber:    0x1337,
			PacketNumberLen: 2,
		}
		hdr, hdrRaw := getHeader(extHdr)
		origHdrRaw := append([]byte{}, hdrRaw...) // save a copy of the header
		firstHdrByte := hdrRaw[0]
		hdrRaw[0] ^= 0xff             // invert the first byte
		hdrRaw[len(hdrRaw)-2] ^= 0xff // invert the packet number
		hdrRaw[len(hdrRaw)-1] ^= 0xff // invert the packet number
		Expect(hdrRaw[0]).ToNot(Equal(firstHdrByte))
		opener := mocks.NewMockOpener(mockCtrl)
		cs.EXPECT().GetOpener(protocol.EncryptionHandshake).Return(opener, nil)
		gomock.InOrder(
			// we're using a 2 byte packet number, so the sample starts at the 3rd payload byte
			opener.EXPECT().DecryptHeader(
				[]byte{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18},
				&hdrRaw[0],
				append(hdrRaw[len(hdrRaw)-2:], []byte{1, 2}...)).Do(func(_ []byte, firstByte *byte, pnBytes []byte) {
				*firstByte ^= 0xff // invert the first byte back
				for i := range pnBytes {
					pnBytes[i] ^= 0xff // invert the packet number bytes
				}
			}),
			opener.EXPECT().Open(gomock.Any(), gomock.Any(), protocol.PacketNumber(0x1337), origHdrRaw).Return([]byte{0}, nil),
		)
		data := hdrRaw
		for i := 1; i <= 100; i++ {
			data = append(data, uint8(i))
		}
		packet, err := unpacker.Unpack(hdr, data)
		Expect(err).ToNot(HaveOccurred())
		Expect(packet.packetNumber).To(Equal(protocol.PacketNumber(0x1337)))
	})

	It("decodes the packet number", func() {
		firstHdr := &wire.ExtendedHeader{
			Header:          wire.Header{DestConnectionID: connID},
			PacketNumber:    0x1337,
			PacketNumberLen: 2,
		}
		opener := mocks.NewMockOpener(mockCtrl)
		cs.EXPECT().GetOpener(protocol.Encryption1RTT).Return(opener, nil).Times(2)
		opener.EXPECT().DecryptHeader(gomock.Any(), gomock.Any(), gomock.Any())
		opener.EXPECT().Open(gomock.Any(), gomock.Any(), firstHdr.PacketNumber, gomock.Any()).Return([]byte{0}, nil)
		packet, err := unpacker.Unpack(getHeader(firstHdr))
		Expect(err).ToNot(HaveOccurred())
		Expect(packet.packetNumber).To(Equal(protocol.PacketNumber(0x1337)))
		// the real packet number is 0x1338, but only the last byte is sent
		secondHdr := &wire.ExtendedHeader{
			Header:          wire.Header{DestConnectionID: connID},
			PacketNumber:    0x38,
			PacketNumberLen: 1,
		}
		// expect the call with the decoded packet number
		opener.EXPECT().DecryptHeader(gomock.Any(), gomock.Any(), gomock.Any())
		opener.EXPECT().Open(gomock.Any(), gomock.Any(), protocol.PacketNumber(0x1338), gomock.Any()).Return([]byte{0}, nil)
		packet, err = unpacker.Unpack(getHeader(secondHdr))
		Expect(err).ToNot(HaveOccurred())
		Expect(packet.packetNumber).To(Equal(protocol.PacketNumber(0x1338)))
	})

	It("unpacks the frames", func() {
		extHdr := &wire.ExtendedHeader{
			Header:          wire.Header{DestConnectionID: connID},
			PacketNumber:    0x1337,
			PacketNumberLen: 2,
		}
		buf := &bytes.Buffer{}
		(&wire.PingFrame{}).Write(buf, protocol.VersionWhatever)
		(&wire.DataBlockedFrame{}).Write(buf, protocol.VersionWhatever)
		hdr, hdrRaw := getHeader(extHdr)
		opener := mocks.NewMockOpener(mockCtrl)
		opener.EXPECT().DecryptHeader(gomock.Any(), gomock.Any(), gomock.Any())
		cs.EXPECT().GetOpener(protocol.Encryption1RTT).Return(opener, nil)
		opener.EXPECT().Open(gomock.Any(), gomock.Any(), extHdr.PacketNumber, hdrRaw).Return(buf.Bytes(), nil)
		packet, err := unpacker.Unpack(hdr, append(hdrRaw, buf.Bytes()...))
		Expect(err).ToNot(HaveOccurred())
		Expect(packet.frames).To(Equal([]wire.Frame{&wire.PingFrame{}, &wire.DataBlockedFrame{}}))
	})
})
