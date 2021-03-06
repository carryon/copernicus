package model

import (
	"encoding/binary"

	"github.com/btcboost/copernicus/utils"
	"github.com/pkg/errors"
	"io"
)

const (
	// SEQUENCE_LOCKTIME_DISABLE_FLAG Below flags apply in the context of BIP 68*/
	// If this flag set, CTxIn::nSequence is NOT interpreted as a
	// relative lock-time. */
	SEQUENCE_LOCKTIME_DISABLE_FLAG = 1 << 31

	// SEQUENCE_LOCKTIME_TYPE_FLAG If CTxIn::nSequence encodes a relative lock-time and this flag
	// is set, the relative lock-time has units of 512 seconds,
	// otherwise it specifies blocks with a granularity of 1.
	SEQUENCE_LOCKTIME_TYPE_FLAG = 1 << 22

	// SEQUENCE_LOCKTIME_MASK If CTxIn::nSequence encodes a relative lock-time, this mask is
	// applied to extract that lock-time from the sequence field.
	SEQUENCE_LOCKTIME_MASK = 0x0000ffff

	ONE_MEGABYTE = 1000000
	/** The maximum allowed size for a transaction, in bytes */

	MAX_TX_SIZE = ONE_MEGABYTE

	// MAX_TX_SIGOPS_COUNT The maximum allowed number of signature check operations per transaction (network rule)
	MAX_TX_SIGOPS_COUNT = 20000

	MAX_MONOY = 21000000

	MAX_STANDARD_VERSION = 2

	MaxTxInSequenceNum uint32 = 0xffffffff
	FreeListMaxItems          = 12500
	MaxMessagePayload         = 32 * 1024 * 1024
	MinTxInPayload            = 9 + utils.HashSize
	MaxTxInPerMessage         = (MaxMessagePayload / MinTxInPayload) + 1
	TxVersion                 = 1
)

type Tx struct {
	Hash     utils.Hash
	LockTime uint32
	Version  int32
	Ins      []*TxIn
	Outs     []*TxOut
	ValState int
}

var scriptPool ScriptFreeList = make(chan []byte, FreeListMaxItems)

func (tx *Tx) AddTxIn(txIn *TxIn) {
	tx.Ins = append(tx.Ins, txIn)
}

func (tx *Tx) AddTxOut(txOut *TxOut) {
	tx.Outs = append(tx.Outs, txOut)
}

func (tx *Tx) RemoveTxIn(txIn *TxIn) {

}

func (tx *Tx) RemoveTxOut(txOut *TxOut) {

}

func (tx *Tx) SerializeSize() int {
	// Version 4 bytes + LockTime 4 bytes + Serialized varint size for the
	// number of transaction inputs and outputs.
	n := 8 + utils.VarIntSerializeSize(uint64(len(tx.Ins))) + utils.VarIntSerializeSize(uint64(len(tx.Outs)))
	for _, txIn := range tx.Ins {
		n += txIn.SerializeSize()
	}
	for _, txOut := range tx.Outs {
		n += txOut.SerializeSize()
	}
	return n
}

func (tx *Tx) Serialize(writer io.Writer) error {
	err := utils.BinarySerializer.PutUint32(writer, binary.LittleEndian, uint32(tx.Version))
	if err != nil {
		return err
	}
	count := uint64(len(tx.Ins))
	err = utils.WriteVarInt(writer, count)
	if err != nil {
		return err
	}
	for _, ti := range tx.Ins {
		err := ti.Serialize(writer, tx.Version)
		if err != nil {
			return err
		}
	}
	count = uint64(len(tx.Outs))
	err = utils.WriteVarInt(writer, count)
	if err != nil {
		return err
	}
	for _, txOut := range tx.Outs {
		err := txOut.Serialize(writer, tx.Version)
		if err != nil {
			return err
		}
	}
	return utils.BinarySerializer.PutUint32(writer, binary.LittleEndian, tx.LockTime)

}

func (tx *Tx) Deserialize(reader io.Reader) (err error) {
	version, err := utils.BinarySerializer.Uint32(reader, binary.LittleEndian)
	if err != nil {
		return err
	}
	tx.Version = int32(version)
	count, err := utils.ReadVarInt(reader)
	if err != nil {
		return err
	}
	if count > uint64(MaxTxInPerMessage) {
		err = errors.Errorf("too many input tx to fit into max message size [count %d , max %d]", count, MaxTxInPerMessage)
		return
	}
	var totalScriptSize uint64

	txIns := make([]TxIn, count)
	tx.Ins = make([]*TxIn, count)
	for i := uint64(0); i < count; i++ {
		txIns[i].PreviousOutPoint = new(OutPoint)
		txIns[i].PreviousOutPoint.Hash = new(utils.Hash)

		txIn := &txIns[i]
		tx.Ins[i] = txIn
		err = txIn.Deserialize(reader, tx.Version)
		if err != nil {
			tx.returnScriptBuffers()
			return
		}
		totalScriptSize += uint64(txIn.Script.Size())
	}
	count, err = utils.ReadVarInt(reader)
	if err != nil {
		tx.returnScriptBuffers()
		return
	}

	txOuts := make([]TxOut, count)
	tx.Outs = make([]*TxOut, count)
	for i := uint64(0); i < count; i++ {
		// The pointer is set now in case a script buffer is borrowed
		// and needs to be returned to the pool on error.

		txOut := &txOuts[i]
		tx.Outs[i] = txOut
		err = txOut.Deserialize(reader, tx.Version)
		if err != nil {
			tx.returnScriptBuffers()
			return
		}
		totalScriptSize += uint64(txOut.Script.Size())
	}

	tx.LockTime, err = utils.BinarySerializer.Uint32(reader, binary.LittleEndian)
	if err != nil {
		tx.returnScriptBuffers()
		return
	}
	tx.returnScriptBuffers()
	return

}

func (tx *Tx) IsCoinBase() bool {
	return len(tx.Ins) == 1 && tx.Ins[0].PreviousOutPoint == nil
}

func (tx *Tx) CheckSelf() (bool, error) {
	if tx.Version > MAX_STANDARD_VERSION || tx.Version < 1 {
		return false, errors.New("error version")
	}
	if len(tx.Ins) == 0 || len(tx.Outs) == 0 {
		return false, errors.New("no inputs or outputs")
	}
	size := tx.SerializeSize()
	if size > MAX_TX_SIZE {
		return false, errors.Errorf("tx size %d > max size %d", size, MAX_TX_SIZE)
	}

	TotalOutValue := int64(0)
	TotalSigOpCount := int64(0)
	TxOutsLen := len(tx.Outs)
	//to do: check txOut's script is
	for i := 0; i < TxOutsLen; i++ {
		txOut := tx.Outs[i]
		if txOut.Value < 0 {
			return false, errors.Errorf("tx out %d's value:%d invalid", i, txOut.Value)
		}
		if txOut.Value > MAX_MONOY {
			return false, errors.Errorf("tx out %d's value:%d invalid", i, txOut.Value)
		}

		TotalOutValue += txOut.Value
		if TotalOutValue > MAX_MONOY {
			return false, errors.Errorf("tx outs' total value:%d from 0 to %d is too large", TotalOutValue, i)
		}

		TotalSigOpCount += int64(txOut.SigOpCount)
		if TotalSigOpCount > MAX_TX_SIGOPS_COUNT {
			return false, errors.Errorf("tx outs' total SigOpCount:%d from 0 to %d is too large", TotalSigOpCount, i)
		}
	}

	//todo: check ins' preout duplicate at the same time
	TxInsLen := len(tx.Ins)
	for i := 0; i < TxInsLen; i++ {
		txIn := tx.Ins[i]
		TotalSigOpCount += int64(txIn.SigOpCount)
		if TotalSigOpCount > MAX_TX_SIGOPS_COUNT {
			return false, errors.Errorf("tx total SigOpCount:%d of all Outs and partial Ins from 0 to %d is too large", TotalSigOpCount, i)
		}
		if txIn.Script.Size() > 1650 {
			return false, errors.Errorf("txIn %d has too long script", i)
		}
		if !txIn.Script.IsPushOnly() {
			return false, errors.Errorf("txIn %d's script is not push script", i)
		}
	}
	return true, nil
}

func (tx *Tx) CheckChain() (int, error) {
	//TxInsLen := len(tx.Ins)
	//for i := 0; i < TxInsLen; i++ {
	//	PreTx := mempool.GetTx(tx.Ins[i].PreviousOutPoint.Hash)
	//	if PreTx {
	//		if PreTx.ValState == TX_ORPHAN {
	//			return TX_ORPHAN, error.new("previous TX is orphan")
	//		}
	//		PreTxsOutTotalMoney += PreTx.Outs[tx.Ins[i].PreviousOutPoint.Index].Value
	//	} else {
	//		PreTx = UTXO.GetTx(tx.Ins[i].PreviousOutPoint.Hash)
	//		if !PreTx {
	//			return TX_ORPHAN, error.new("UTXO has no previous tx")
	//		}
	//		PreTxsOutTotalMoney += PreTx.Outs[tx.Ins[i].PreviousOutPoint.Index].Value
	//	}
	//}
	//TxOutsLen := len(tx.Outs)
	//for j := 0; j < TxOutsLen; j++ {
	//	TotalOutsMoney += tx.Outs[j].Value
	//}
	//if PreTxsOutTotalMoney < TotalOutsMoney {
	//	return TX_INVALID, error.new("Ins' money < outs' money")
	//}
	//
	//for k := 0; k < TxInsLen; k++ {
	//	tx.Ins[k].Script.Eval()
	//}
	return 0, nil
}

func (tx *Tx) returnScriptBuffers() {
	for _, txIn := range tx.Ins {
		if txIn == nil || txIn.Script == nil {
			continue
		}
		scriptPool.Return(txIn.Script.bytes)
	}
	for _, txOut := range tx.Outs {
		if txOut == nil || txOut.Script == nil {
			continue
		}
		scriptPool.Return(txOut.Script.bytes)
	}
}
func (tx *Tx) Copy() *Tx {
	newTx := Tx{
		Version:  tx.Version,
		LockTime: tx.LockTime,
		Ins:      make([]*TxIn, 0, len(tx.Ins)),
		Outs:     make([]*TxOut, 0, len(tx.Outs)),
	}
	newTx.Hash = tx.Hash

	for _, txOut := range tx.Outs {
		scriptLen := len(txOut.Script.bytes)
		newOutScript := make([]byte, scriptLen)
		copy(newOutScript, txOut.Script.bytes[:scriptLen])

		newTxOut := TxOut{
			Value:  txOut.Value,
			Script: NewScriptRaw(newOutScript),
		}
		newTx.Outs = append(newTx.Outs, &newTxOut)
	}
	for _, txIn := range tx.Ins {
		var buf utils.Hash
		newOutPoint := OutPoint{Hash: &buf}
		newOutPoint.Hash.SetBytes(txIn.PreviousOutPoint.Hash[:])
		newOutPoint.Index = txIn.PreviousOutPoint.Index
		scriptLen := txIn.Script.Size()
		newScript := make([]byte, scriptLen)
		copy(newScript[:], txIn.Script.bytes[:scriptLen])
		newTxTmp := TxIn{
			Sequence:         txIn.Sequence,
			PreviousOutPoint: &newOutPoint,
			Script:           NewScriptRaw(newScript),
		}
		newTx.Ins = append(newTx.Ins, &newTxTmp)
	}
	return &newTx

}

func NewTx() *Tx {
	return &Tx{LockTime: 0, Version: TxVersion}
}
