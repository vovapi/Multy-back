/*
Copyright 2017 Idealnaya rabota LLC
Licensed under Multy.io license.
See LICENSE for details
*/
package btc

import (
	"fmt"
	"time"

	"github.com/Appscrunch/Multy-back/store"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	mgo "gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

func newEmptyTx(userID string) store.TxRecord {
	return store.TxRecord{
		UserID:       userID,
		Transactions: []store.MultyTX{},
	}
}
func newAddresAmount(address string, amount int64) store.AddresAmount {
	return store.AddresAmount{
		Address: address,
		Amount:  amount,
	}
}

//func newMultyTX(tx store.MultyTX) store.MultyTX {
//	return store.MultyTX{
//		TxID:              tx.TxID,
//		TxHash:            tx.TxHash,
//		TxOutScript:       tx.TxOutScript,
//		TxAddress:         tx,
//		TxStatus:          txStatus,
//		TxOutAmount:       txOutAmount,
//		TxOutID:           txOutID,
//		WalletIndex:       walletindex,
//		BlockTime:         blockTime,
//		BlockHeight:       blockHeight,
//		TxFee:             fee,
//		MempoolTime:       mempoolTime,
//		StockExchangeRate: stockexchangerate,
//		TxInputs:          inputs,
//		TxOutputs:         outputs,
//	}
//}

func rawTxByTxid(txid string) (*btcjson.TxRawResult, error) {
	hash, err := chainhash.NewHashFromStr(txid)
	if err != nil {
		return nil, err
	}
	previousTxVerbose, err := rpcClient.GetRawTransactionVerbose(hash)
	if err != nil {
		return nil, err
	}
	return previousTxVerbose, nil
}

func fetchWalletAndAddressIndexes(wallets []store.Wallet, address string) (int, int) {
	var walletIndex int
	var addressIndex int
	for _, wallet := range wallets {
		for i, addr := range wallet.Adresses {
			if addr.Address == address {
				walletIndex = wallet.WalletIndex
				addressIndex = i
				break
			}
		}
	}
	return walletIndex, addressIndex
}

func txInfo(txVerbose *btcjson.TxRawResult) ([]store.AddresAmount, []store.AddresAmount, int64, error) {

	inputs := []store.AddresAmount{}
	outputs := []store.AddresAmount{}
	var inputSum float64
	var outputSum float64

	for _, out := range txVerbose.Vout {
		for _, address := range out.ScriptPubKey.Addresses {
			amount := int64(out.Value * 100000000)
			outputs = append(outputs, newAddresAmount(address, amount))
		}
		outputSum += out.Value
	}
	for _, input := range txVerbose.Vin {
		hash, err := chainhash.NewHashFromStr(input.Txid)
		if err != nil {
			log.Errorf("txInfo:chainhash.NewHashFromStr: %s", err.Error())
			return nil, nil, 0, err
		}
		previousTxVerbose, err := rpcClient.GetRawTransactionVerbose(hash)
		if err != nil {
			log.Errorf("txInfo:rpcClient.GetRawTransactionVerbose: %s", err.Error())
			return nil, nil, 0, err
		}

		for _, address := range previousTxVerbose.Vout[input.Vout].ScriptPubKey.Addresses {
			amount := int64(previousTxVerbose.Vout[input.Vout].Value * 100000000)
			inputs = append(inputs, newAddresAmount(address, amount))
		}
		inputSum += previousTxVerbose.Vout[input.Vout].Value
	}
	fee := int64((inputSum - outputSum) * 100000000)

	return inputs, outputs, fee, nil
}

/*

Main process BTC transaction method

can be called from:
- Mempool
- New block
- Resync

*/

func processTransaction(blockChainBlockHeight int64, txVerbose *btcjson.TxRawResult) {
	var multyTx *store.MultyTX = parseRawTransaction(blockChainBlockHeight, txVerbose)
	if multyTx != nil {
		transactions := splitTransaction(*multyTx, blockChainBlockHeight)

		for _, transaction := range transactions {

			rates, err := GetLatestExchangeRate()
			if err != nil {
				log.Errorf("processTransaction:ExchangeRates: %s", err.Error())
			}
			transaction.StockExchangeRate = rates
			updateWalletAndAddressDate(transaction)

			saveMultyTransaction(transaction)
			sendNotifyToClients(transaction)
		}
	}
}

/*
This method should parse raw transaction from BTC node

_________________________
Inputs:
* blockChainBlockHeight int64 - could be:
-1 in case of mempool call
>1 in case of block transaction
max chain height in case of resync

*txVerbose - raw BTC transaction
_________________________
Output:
* multyTX - multy transaction Structure

*/
func parseRawTransaction(blockChainBlockHeight int64, txVerbose *btcjson.TxRawResult) *store.MultyTX {
	multyTx := store.MultyTX{}

	err := parseInputs(txVerbose, blockChainBlockHeight, &multyTx)
	if err != nil {
		log.Errorf("parseRawTransaction:parseInputs: %s", err.Error())
	}

	err = parseOutputs(txVerbose, blockChainBlockHeight, &multyTx)
	if err != nil {
		log.Errorf("parseRoawTransaction:parseOutputs: %s", err.Error())
	}

	if multyTx.TxID != "" {
		return &multyTx
	} else {
		return nil
	}
}

/*
This method need if we have one transaction with more the one u wser'sallet
That means that from one btc transaction we should build more the one Multy Transaction
*/
func splitTransaction(multyTx store.MultyTX, blockHeight int64) []store.MultyTX {
	transactions := make([]store.MultyTX, 1)

	currentBlockHeight, err := rpcClient.GetBlockCount()
	if err != nil {
		log.Errorf("splitTransaction:getBlockCount: %s", err.Error())
	}

	blockDiff := currentBlockHeight - blockHeight

	//This is implementatios for single wallet transaction for multi addresses not for multi wallets!
	if multyTx.WalletsInput != nil && multyTx.WalletsOutput != nil && len(multyTx.WalletsInput) > 0 && len(multyTx.WalletsOutput) > 0 {
		// outgoingTx := multyTx
		// incomingTx := multyTx

		outgoingTx := newEntity(multyTx)
		incomingTx := newEntity(multyTx)

		setTransactionStatus(&outgoingTx, blockDiff, currentBlockHeight, true)
		setTransactionStatus(&incomingTx, blockDiff, currentBlockHeight, false)
		transactions = append(transactions, outgoingTx, incomingTx)
	} else {
		transactions = append(transactions, multyTx)
	}

	return transactions
}

func newEntity(multyTx store.MultyTX) store.MultyTX {
	newTx := store.MultyTX{
		TxID:              multyTx.TxID,
		TxHash:            multyTx.TxHash,
		TxOutScript:       multyTx.TxOutScript,
		TxAddress:         multyTx.TxAddress,
		TxStatus:          multyTx.TxStatus,
		TxOutAmount:       multyTx.TxOutAmount,
		TxOutIndexes:      multyTx.TxOutIndexes,
		TxInAmount:        multyTx.TxInAmount,
		TxInIndexes:       multyTx.TxInIndexes,
		BlockTime:         multyTx.BlockTime,
		BlockHeight:       multyTx.BlockHeight,
		TxFee:             multyTx.TxFee,
		MempoolTime:       multyTx.MempoolTime,
		StockExchangeRate: multyTx.StockExchangeRate,
		TxInputs:          multyTx.TxInputs,
		TxOutputs:         multyTx.TxOutputs,
		WalletsInput:      multyTx.WalletsInput,
		WalletsOutput:     multyTx.WalletsOutput,
	}
	return newTx
}

func saveMultyTransaction(tx store.MultyTX) {
	//TODO save it
	//TODO update wallet date
	//TODO update address date

	// User have this transaction but with another status.
	// Update statsus, block height and block time.
	for _, walletInput := range tx.WalletsInput {

		sel = bson.M{"userid": walletInput.UserId, "transactions.txid": tx.TxID, "transactions.txaddress": walletInput.Address}
		update = bson.M{
			"$set": bson.M{
				"transactions.$.txstatus":    tx.TxStatus,
				"transactions.$.blockheight": tx.BlockHeight,
				"transactions.$.blocktime":   tx.BlockTime,
			},
		}
		err = txsData.Update(sel, update)

		if err != mgo.ErrNotFound {
			sel := bson.M{"userid": walletInput.UserId}
			update := bson.M{"$push": bson.M{"transactions": tx}}
			err := txsData.Update(sel, update)
			if err != nil {
				log.Errorf("parseInput.Update add new tx to user: %s", err.Error())
			}
		}

	}

	for _, walletOutput := range tx.WalletsOutput {
		sel = bson.M{"userid": walletOutput.UserId, "transactions.txid": tx.TxID, "transactions.txaddress": walletOutput.Address}
		update = bson.M{
			"$set": bson.M{
				"transactions.$.txstatus":    tx.TxStatus,
				"transactions.$.blockheight": tx.BlockHeight,
				"transactions.$.blocktime":   tx.BlockTime,
			},
		}
		err = txsData.Update(sel, update)

		if err != mgo.ErrNotFound {
			sel := bson.M{"userid": walletOutput.UserId}
			update := bson.M{"$push": bson.M{"transactions": tx}}
			err := txsData.Update(sel, update)
			if err != nil {
				log.Errorf("parseInput.Update add new tx to user: %s", err.Error())
			}
		}
	}

	/*
		sel = bson.M{"userid": user.UserID, "transactions.txid": txVerbose.Txid, "transactions.txaddress": address}
		update = bson.M{
			"$set": bson.M{
				"transactions.$.txstatus":    txStatus,
				"transactions.$.blockheight": blockHeight,
				"transactions.$.blocktime":   blockTimeUnix,
			},
		}

		err = txsData.Update(sel, update)
		if err != nil {
			log.Errorf("parseInput:outputsData.Insert case nil: %s", err.Error())
		}

		newTx := newMultyTX(txVerbose.Txid, txVerbose.Hash, output.ScriptPubKey.Hex, address, txStatus, int(output.N), walletIndex, txOutAmount, blockTime, blockHeight, fee, blockTimeUnix, exRates, inputs, outputs)
		sel = bson.M{"userid": user.UserID}
		update := bson.M{"$push": bson.M{"transactions": newTx}}
		err = txsData.Update(sel, update)
		if err != nil {
			log.Errorf("parseInput.Update add new tx to user: %s", err.Error())
		}
	*/
}

func sendNotifyToClients(tx store.MultyTX) {

	for _, walletOutput := range tx.WalletsOutput {
		txMsq := BtcTransactionWithUserID{
			UserID: walletOutput.UserId,
			NotificationMsg: &BtcTransaction{
				TransactionType: tx.TxStatus,
				Amount:          tx.TxOutAmount,
				TxID:            tx.TxID,
				Address:         walletOutput.Address,
			},
		}
		sendNotifyToClients(&txMsq)
	}

	for _, walletInput := range tx.WalletsInput {
		txMsq := BtcTransactionWithUserID{
			UserID: walletInput.UserId,
			NotificationMsg: &BtcTransaction{
				TransactionType: tx.TxStatus,
				Amount:          tx.TxInAmount,
				TxID:            tx.TxID,
				Address:         walletInput.Address,
			},
		}
		sendNotifyToClients(&txMsq)
	}

	//TODO make it correct
	//func sendNotifyToClients(txMsq *BtcTransactionWithUserID) {
	//	newTxJSON, err := json.Marshal(txMsq)
	//	if err != nil {
	//		log.Errorf("sendNotifyToClients: [%+v] %s\n", txMsq, err.Error())
	//		return
	//	}
	//
	//	err = nsqProducer.Publish(TopicTransaction, newTxJSON)
	//	if err != nil {
	//		log.Errorf("nsq publish new transaction: [%+v] %s\n", txMsq, err.Error())
	//		return
	//	}
	//	return
}

func parseInputs(txVerbose *btcjson.TxRawResult, blockHeight int64, multyTx *store.MultyTX) error {
	//NEW LOGIC
	user := store.User{}
	//Ranging by inputs
	for _, input := range txVerbose.Vin {

		//getting previous verbose transaction from BTC Node for checking addresses
		previousTxVerbose, err := rawTxByTxid(input.Txid)
		if err != nil {
			log.Errorf("parseInput:rawTxByTxid: %s", err.Error())
			continue
		}

		for _, txInAddress := range previousTxVerbose.Vout[input.Vout].ScriptPubKey.Addresses {
			query := bson.M{"wallets.addresses.address": txInAddress}

			err := usersData.Find(query).One(&user)
			if err != nil {
				continue
				// is not our user
			}
			fmt.Println("[ITS OUR USER] ", user.UserID)

			walletIndex, addressIndex := fetchWalletAndAddressIndexes(user.Wallets, txInAddress)

			txInAmount := int64(100000000 * previousTxVerbose.Vout[input.Vout].Value)

			currentWallet := store.WalletForTx{UserId: user.UserID, WalletIndex: walletIndex}

			if multyTx.TxInputs == nil {
				multyTx.TxInputs = make([]store.AddresAmount, 2)
			}

			if multyTx.WalletsInput == nil {
				multyTx.WalletsInput = make([]store.WalletForTx, 2)
			}

			currentWallet.Address = store.AddressWorWallet{Address: txInAddress, AddressIndex: addressIndex, Amount: txInAmount}
			multyTx.WalletsInput = append(multyTx.WalletsInput, currentWallet)

			multyTx.TxInputs = append(multyTx.TxInputs, store.AddresAmount{Address: txInAddress, Amount: txInAmount})

			//
			//if len(multyTx.WalletsInput) > 0{
			//	var haveTheSameWalletIndex = false
			//	//Check if we already have the same wallet index
			//	for _, walletInForTx := range multyTx.WalletsInput{
			//		if walletInForTx.WalletIndex == currentWallet.WalletIndex{
			//			haveTheSameWalletIndex = true
			//		}
			//	}
			//	if !haveTheSameWalletIndex{
			//		//This is not stored wallet
			//
			//	}
			//} else {
			//	currentWallet.Address = store.AddressWorWallet{Address:txInAddress, AddressIndex:addressIndex, Amount:txInAmount}
			//	multyTx.WalletsInput = append(multyTx.WalletsInput, currentWallet)
			//}

			multyTx.TxID = txVerbose.Txid
			multyTx.TxHash = txVerbose.Hash

		}

	}

	return nil

	//OLD LOGIC
	//user := store.User{}
	//blockTimeUnix := time.Now().Unix()
	//
	////Ranging by inputs
	//for _, input := range txVerbose.Vin {
	//
	//	//getting previous verbose transaction from BTC Node for checking addresses
	//	previousTxVerbose, err := rawTxByTxid(input.Txid)
	//	if err != nil {
	//		log.Errorf("parseInput:rawTxByTxid: %s", err.Error())
	//		continue
	//	}
	//
	//	for _, address := range previousTxVerbose.Vout[input.Vout].ScriptPubKey.Addresses {
	//		query := bson.M{"wallets.addresses.address": address}
	//		// Is it's our user transaction.
	//		err := usersData.Find(query).One(&user)
	//		if err != nil {
	//			continue
	//			// Is not our user.
	//		}
	//
	//		log.Debugf("[ITS OUR USER] %s", user.UserID)
	//
	//		inputs, outputs, fee, err := txInfo(txVerbose)
	//		if err != nil {
	//			log.Errorf("parseInput:txInfo:input: %s", err.Error())
	//			continue
	//		}
	//
	//
	//		walletIndex := fetchWalletIndex(user.Wallets, address)
	//
	//
	//
	//		// Is our user already have this transactions.
	//		sel := bson.M{"userid": user.UserID, "transactions.txid": txVerbose.Txid, "transactions.txaddress": address}
	//		err = txsData.Find(sel).One(nil)
	//		if err == mgo.ErrNotFound {
	//			// User have no transaction like this. Add to DB.
	//			txOutAmount := int64(100000000 * previousTxVerbose.Vout[input.Vout].Value)
	//
	//			// Set bloct time -1 if tx from mempool.
	//			blockTime := blockTimeUnix
	//			if blockHeight == -1 {
	//				blockTime = int64(-1)
	//			}
	//
	//			newTx := newMultyTX(txVerbose.Txid, txVerbose.Hash, previousTxVerbose.Vout[input.Vout].ScriptPubKey.Hex, address, txStatus, int(previousTxVerbose.Vout[input.Vout].N), walletIndex, txOutAmount, blockTime, blockHeight, fee, blockTimeUnix, exRates, inputs, outputs)
	//			sel = bson.M{"userid": user.UserID}
	//			update := bson.M{"$push": bson.M{"transactions": newTx}}
	//			err = txsData.Update(sel, update)
	//			if err != nil {
	//				log.Errorf("parseInput:txsData.Update add new tx to user: %s", err.Error())
	//			}
	//			continue
	//		} else if err != nil && err != mgo.ErrNotFound {
	//			log.Errorf("parseInput:txsData.Find: %s", err.Error())
	//			continue
	//		}
	//
	//		// User have this transaction but with another status.
	//		// Update statsus, block height and block time.
	//		sel = bson.M{"userid": user.UserID, "transactions.txid": txVerbose.Txid, "transactions.txaddress": address}
	//		update = bson.M{
	//			"$set": bson.M{
	//				"transactions.$.txstatus":    txStatus,
	//				"transactions.$.blockheight": blockHeight,
	//				"transactions.$.blocktime":   blockTimeUnix,
	//			},
	//		}
	//		err = txsData.Update(sel, update)
	//		if err != nil {
	//			log.Errorf("parseInput:txsData.Update: %s", err.Error())
	//		}
	//	}
	//}
	//return nil
}

func parseOutputs(txVerbose *btcjson.TxRawResult, blockHeight int64, multyTx *store.MultyTX) error {

	user := store.User{}

	for _, output := range txVerbose.Vout {
		for _, txOutAddress := range output.ScriptPubKey.Addresses {
			query := bson.M{"wallets.addresses.address": txOutAddress}

			err := usersData.Find(query).One(&user)
			if err != nil {
				continue
				// is not our user
			}
			fmt.Println("[ITS OUR USER] ", user.UserID)

			walletIndex, addressIndex := fetchWalletAndAddressIndexes(user.Wallets, txOutAddress)

			currentWallet := store.WalletForTx{UserId: user.UserID, WalletIndex: walletIndex}

			if multyTx.TxOutputs == nil {
				multyTx.TxOutputs = make([]store.AddresAmount, 2)
			}

			if multyTx.WalletsOutput == nil {
				multyTx.WalletsOutput = make([]store.WalletForTx, 2)
			}

			currentWallet.Address = store.AddressWorWallet{Address: txOutAddress, AddressIndex: addressIndex, Amount: int64(100000000 * output.Value)}
			multyTx.WalletsOutput = append(multyTx.WalletsOutput, currentWallet)

			multyTx.TxOutputs = append(multyTx.TxOutputs, store.AddresAmount{Address: txOutAddress, Amount: int64(100000000 * output.Value)})

			//if len(multyTx.WalletsOutput) > 0{
			//	var haveTheSameWalletIndex = false
			//	//Check if we already have the same wallet index
			//	for _, walletOutForTx := range multyTx.WalletsOutput{
			//		if walletOutForTx.WalletIndex == currentWallet.WalletIndex{
			//			haveTheSameWalletIndex = true
			//		}
			//	}
			//	if !haveTheSameWalletIndex{
			//		//This is not stored wallet
			//		currentWallet.Address = store.AddressWorWallet{Address:txOutAddress, AddressIndex:addressIndex, Amount:int64(100000000 * output.Value)}
			//		multyTx.WalletsOutput = append(multyTx.WalletsOutput, currentWallet)
			//
			//		multyTx.TxOutputs = append(multyTx.TxOutputs, store.AddresAmount{Address:txOutAddress, Amount:int64(100000000 * output.Value)})
			//
			//
			//	}
			//} else {
			//	currentWallet.Address = store.AddressWorWallet{Address:txOutAddress, AddressIndex:addressIndex, Amount:int64(100000000 * output.Value)}
			//	multyTx.WalletsOutput = append(multyTx.WalletsOutput, currentWallet)
			//}

			multyTx.TxID = txVerbose.Txid
			multyTx.TxHash = txVerbose.Hash

		}
	}
	return nil
}

func GetLatestExchangeRate() ([]store.ExchangeRatesRecord, error) {
	selGdax := bson.M{
		"stockexchange": "Gdax",
	}
	selPoloniex := bson.M{
		"stockexchange": "Poloniex",
	}
	stocksGdax := store.ExchangeRatesRecord{}
	err := exRate.Find(selGdax).Sort("-timestamp").One(&stocksGdax)
	if err != nil {
		return nil, err
	}

	stocksPoloniex := store.ExchangeRatesRecord{}
	err = exRate.Find(selPoloniex).Sort("-timestamp").One(&stocksPoloniex)
	if err != nil {
		return nil, err
	}
	return []store.ExchangeRatesRecord{stocksPoloniex, stocksGdax}, nil

}

func updateWalletAndAddressDate(tx store.MultyTX) {
	blockTimeUnix := time.Now().Unix()
	//TODO make all nesessary updates HERE

	//TODO this code is just example of useage
	//TODO change it to correct structure!

	for _, walletOutput := range tx.WalletsOutput {

		// update addresses last action time
		sel = bson.M{"userID": walletOutput.UserId, "wallets.addresses.address": walletOutput.Address}
		update = bson.M{
			"$set": bson.M{
				"wallets.$.addresses.$[].lastActionTime": time.Now().Unix(),
			},
		}
		err = usersData.Update(sel, update)
		if err != nil {
			log.Errorf("updateWalletAndAddressDate:usersData.Update: %s", err.Error())
		}

		// update wallets last action time
		// Set status to OK if some money transfered to this address
		sel := bson.M{"userID": walletOutput.UserId, "wallets.walletIndex": walletOutput.WalletIndex}
		update := bson.M{
			"$set": bson.M{
				"wallets.$.status":         store.WalletStatusOK,
				"wallets.$.lastActionTime": time.Now().Unix(),
			},
		}
		err = usersData.Update(sel, update)
		if err != nil {
			log.Errorf("updateWalletAndAddressDate:usersData.Update: %s", err.Error())
		}

	}

	for _, walletInput := range tx.WalletsInput {
		// update addresses last action time
		sel = bson.M{"userID": walletOutput.UserId, "wallets.addresses.address": walletOutput.Address}
		update = bson.M{
			"$set": bson.M{
				"wallets.$.addresses.$[].lastActionTime": time.Now().Unix(),
			},
		}
		err = usersData.Update(sel, update)
		if err != nil {
			log.Errorf("updateWalletAndAddressDate:usersData.Update: %s", err.Error())
		}

		// update wallets last action time
		sel := bson.M{"userID": walletOutput.UserId, "wallets.walletIndex": walletOutput.WalletIndex}
		update := bson.M{
			"$set": bson.M{
				"wallets.$.lastActionTime": time.Now().Unix(),
			},
		}
		err = usersData.Update(sel, update)
		if err != nil {
			log.Errorf("updateWalletAndAddressDate:usersData.Update: %s", err.Error())
		}
	}

	/*
		// Update wallets last action time on every new transaction.
		// Set status to OK if some money transfered to this address
		sel := bson.M{"userID": user.UserID, "wallets.walletIndex": walletIndex}
		update := bson.M{
			"$set": bson.M{
				"wallets.$.status":         store.WalletStatusOK,
				"wallets.$.lastActionTime": time.Now().Unix(),
			},
		}
		err = usersData.Update(sel, update)
		if err != nil {
			log.Errorf("parseOutput:restClient.userStore.Update: %s", err.Error())
		}

		// Update wallets last action time on every new transaction.
		sel := bson.M{"userID": user.UserID, "wallets.walletIndex": walletIndex}
		update := bson.M{
			"$set": bson.M{
				"wallets.$.lastActionTime": time.Now().Unix(),
			},
		}
		err := usersData.Update(sel, update)
		if err != nil {
			log.Errorf("parseOutput:restClient.userStore.Update: %s", err.Error())
		}

		// Update address last action time on every new transaction.
		sel = bson.M{"userID": user.UserID, "wallets.addresses.address": address}
		update = bson.M{
			"$set": bson.M{
				"wallets.$.addresses.$[].lastActionTime": time.Now().Unix(),
			},
		}
		err = usersData.Update(sel, update)
		if err != nil {
			log.Errorf("parseOutput:restClient.userStore.Update: %s", err.Error())
		}
	*/
}

func setTransactionStatus(tx *store.MultyTX, blockDiff int64, currentBlockHeight int64, fromInput bool) {
	if blockDiff > currentBlockHeight {
		//This call was made from memPool
		if fromInput {
			tx.TxStatus = TxStatusAppearedInMempoolOutcoming
		} else {
			tx.TxStatus = TxStatusAppearedInMempoolIncoming
		}
	} else if blockDiff >= 0 && blockDiff < 6 {
		//This call was made from block or resync
		//Transaction have no enough confirmations
		if fromInput {
			tx.TxStatus = TxStatusAppearedInBlockOutcoming
		} else {
			tx.TxStatus = TxStatusAppearedInBlockIncoming
		}
	} else if blockDiff >= 6 && blockDiff < currentBlockHeight {
		//This call was made from resync
		//Transaction have enough confirmations
		if fromInput {
			tx.TxStatus = TxStatusInBlockConfirmedOutcoming
		} else {
			tx.TxStatus = TxStatusInBlockConfirmedIncoming
		}
	}
}