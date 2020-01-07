package tenantdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/boltdb/bolt"
	"github.com/gorilla/mux"
	"net/http"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
)

var log = logf.Log.WithName("db")

type TenantData struct {
	MasterKubeConfig []string `json:"masterKubeConfig"`
	TargetKubeConfig []string `json:"targetKubeConfig"`
	MasterSA         []string `json:"masterSA"`
	TargetSA         []string `json:"targetSA"`
}

const TenantKVStoreNS = "TenantDB"

var TenantStore *bolt.DB

func homeLink(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Welcome to TenantDB !")
}

func initapi() {
	router := mux.NewRouter().StrictSlash(true)
	router.HandleFunc("/tenants/{name}", getOne).Methods("GET")
	http.ListenAndServe(":8090", router)
}

func getOne(w http.ResponseWriter, r *http.Request) {
	// Get the ID from the url
	temp := mux.Vars(r)["name"]
	dummystr := fmt.Sprintf("Querying Tenant db for %s", temp)
	log.Info(dummystr)
	tdb := TenantGetKey(temp)
	json.NewEncoder(w).Encode(tdb)
}

func ConnecttoBolt() {
	var err error
	log.Info("Opening boltdb")

	TenantStore, err = bolt.Open("/tenant.db", 0644, nil)
	if err != nil {
		panic(err)
	}
	go initapi()
}

func CreateTenantKey(name string) string {
	return name
}

func TenantDeleteKey(name string) error {
	return TenantStore.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(TenantKVStoreNS))
		if bucket != nil {
			return bucket.Delete([]byte(CreateTenantKey(name)))
		}
		return nil
	})
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

func TenantList() {
	tdb := &TenantData{}
	TenantStore.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte(TenantKVStoreNS)).Cursor()
		if c != nil {
			for k, v := c.First(); k != nil; k, v = c.Next() {
				errunmar := json.Unmarshal(v, tdb)
				if errunmar != nil {
					return nil
				}
				fmt.Printf("Key:%s Value:%#v\n", k, tdb)
			}
		}
		return nil
	})
}
