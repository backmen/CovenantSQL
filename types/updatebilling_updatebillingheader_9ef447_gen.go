package types

// Code generated by github.com/CovenantSQL/HashStablePack DO NOT EDIT.

import (
	hsp "github.com/CovenantSQL/HashStablePack/marshalhash"
)

// MarshalHash9ef447 marshals for hash
func (z *UpdateBillingHeader) MarshalHash9ef447() (o []byte, err error) {
	var b []byte
	o = hsp.Require(b, z.Msgsize9ef447())
	// map header, size 5
	o = append(o, 0x85)
	if oTemp, err := z.Nonce.MarshalHash(); err != nil {
		return nil, err
	} else {
		o = hsp.AppendBytes(o, oTemp)
	}
	// map header, size 2
	o = append(o, 0x82)
	o = hsp.AppendUint32(o, z.Range.From)
	o = hsp.AppendUint32(o, z.Range.To)
	if oTemp, err := z.Receiver.MarshalHash(); err != nil {
		return nil, err
	} else {
		o = hsp.AppendBytes(o, oTemp)
	}
	o = hsp.AppendArrayHeader(o, uint32(len(z.Users)))
	for za0001 := range z.Users {
		if z.Users[za0001] == nil {
			o = hsp.AppendNil(o)
		} else {
			if oTemp, err := z.Users[za0001].MarshalHash(); err != nil {
				return nil, err
			} else {
				o = hsp.AppendBytes(o, oTemp)
			}
		}
	}
	o = hsp.AppendInt32(o, z.Version)
	return
}

// Msgsize9ef447 returns an upper bound estimate of the number of bytes occupied by the serialized message
func (z *UpdateBillingHeader) Msgsize9ef447() (s int) {
	s = 1 + 6 + z.Nonce.Msgsize() + 6 + 1 + 5 + hsp.Uint32Size + 3 + hsp.Uint32Size + 9 + z.Receiver.Msgsize() + 6 + hsp.ArrayHeaderSize
	for za0001 := range z.Users {
		if z.Users[za0001] == nil {
			s += hsp.NilSize
		} else {
			s += z.Users[za0001].Msgsize()
		}
	}
	s += 2 + hsp.Int32Size
	return
}
