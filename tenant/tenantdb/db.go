package tenantdb

import (
	"encoding/json"
	"errors"
	"github.com/boltdb/bolt"
)

type TenantData struct {
	MasterKubeConfig string   `json:"masterKubeConfig"`
	TargetKubeConfig []string `json:"targetKubeConfig"`
	MasterSA         string   `json:"masterSA"`
	TargetSA         []string `json:"targetSA"`
}

const TenantKVStoreNS = "TenantDB"

var TenantStore *bolt.DB

func ConnecttoBolt() {
	var err error
	TenantStore, err = bolt.Open("/tenant.db", 0644, nil)
	if err != nil {
		panic(err)
	}
}

func CreateTenantKey(name string) string {
	return name
}

func TenantStoreKey(name string, val *TenantData) error {
	temp, err := json.Marshal(val)
	if err != nil {
		return errors.New("Unable to Marshal TenantDB struct")
	}
	// store some data
	err = TenantStore.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(TenantKVStoreNS))
		if err != nil {
			return err
		}

		errb := bucket.Put([]byte(name), temp)
		if errb != nil {
			return errb
		}
		return nil
	})

	return nil
}

func TenantGetKey(name string) *TenantData {
	var resp []byte
	tdb := &TenantData{}

	key := CreateTenantKey(name)
	// retrieve the data
	_ = TenantStore.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(TenantKVStoreNS))
		if bucket == nil {
			return errors.New("Bucket not found!")
		}
		resp = bucket.Get([]byte(key))
		return nil
	})

	errunmar := json.Unmarshal(resp, tdb)
	if errunmar != nil {
		return nil
	}
	return tdb
}
