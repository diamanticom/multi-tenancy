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
	MasterSA         []string `json:"masterSA"`
	TargetSA         []string `json:"targetSA"`
	TargetKubeConfig []string `json:"targetKubeConfig"`
}

const TenantKVStoreNS = "TenantDB"

var TenantStore *bolt.DB

func homeLink(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Welcome to TenantDB !")
}

func initapi() {
	router := mux.NewRouter().StrictSlash(true)
	router.HandleFunc("/tenants/{name}/user/{user}", gettenant).Methods("GET")
	go http.ListenAndServe(":8090", router)
}

func gettenant(w http.ResponseWriter, r *http.Request) {
	// Get the ID from the url
	name := mux.Vars(r)["name"]
	user := mux.Vars(r)["user"]
	dummystr := fmt.Sprintf("Querying Tenant db for %s for user %s", name, user)
	log.Info(dummystr)
	tdb := TenantGetKey(name, user)
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

func CreateTenantKey(name string, username string) string {
	return name + "#" + username
}

func TenantDeleteKey(name string, username string) error {
	return TenantStore.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(TenantKVStoreNS))
		if bucket != nil {
			return bucket.Delete([]byte(CreateTenantKey(name, username)))
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

func TenantGetKey(name string, user string) *TenantData {
	var resp []byte
	tdb := &TenantData{}

	key := CreateTenantKey(name, user)
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
