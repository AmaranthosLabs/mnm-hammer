// Copyright 2017 Liam Breck
//
// This file is part of the "mnm" software. Anyone may redistribute mnm and/or modify
// it under the terms of the GNU Lesser General Public License version 3, as published
// by the Free Software Foundation. See www.gnu.org/licenses/
// Mnm is distributed WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See said License for details.

package slib

import (
   "runtime/debug"
   "hash/crc32"
   "fmt"
   "io"
   "encoding/json"
   "os"
   "path"
   "strings"
   "sync"
)

const kStorageDir = "store/"
const kServiceDir = kStorageDir + "svc/"
const kStateDir   = kStorageDir + "state/"
const UploadDir   = kStorageDir + "upload/"

var sServicesDoor sync.RWMutex
var sServices = make(map[string]*tService)
var sServiceStartFn func(string)

var sCrc32c = crc32.MakeTable(crc32.Castagnoli)


type Header struct {
   Op string
   Id, MsgId string
   Uid, NodeId string
   Info string
   From string
   Posted string
   DataLen, DataHead int64
   SubHead struct {
      ThreadId string
      Subject string
   }
}

func (o *Header) Check() bool {
   return true
}

func (o *Header) CheckSub() bool {
   return true
}

type Update struct {
   Op string
   Thread *struct {
      Id string
      Subject string
      Data string
      New bool
   }
   Navigate *struct {
      History int
   }
   Tab *struct {
      Type int8
      Term string
      PosFor int8
      Pos int
   }
   Service *tService
}

type SendRecord struct {
   Head Msg
   Data []byte
   Files []string
}

type Msg map[string]interface{}


func Init(iFn func(string)) {
   for _, aDir := range [...]string{UploadDir, kServiceDir, kStateDir} {
      err := os.MkdirAll(aDir, 0700)
      if err != nil { quit(err) }
   }
   initStates()
   initServices(iFn)
}

func initServices(iFn func(string)) {
   var err error
   aSvcs, err := readDirNames(kServiceDir)
   if err != nil { quit(err) }

   for _, aSvc := range aSvcs {
      if strings.HasSuffix(aSvc, ".tmp") {
         err = os.RemoveAll(svcDir(aSvc))
         if err != nil { quit(err) }
         continue
      }
      completePending(aSvc)
      err = resolveTmpFile(cfgFile(aSvc) + ".tmp")
      if err != nil { quit(err) }
      aService := &tService{}
      var aFd *os.File
      aFd, err = os.Open(cfgFile(aSvc))
      if err != nil { quit(err) }
      err = json.NewDecoder(aFd).Decode(aService)
      aFd.Close()
      if err != nil { quit(err) }
      sServices[aSvc] = aService
   }
   if sServices["test"] == nil {
      err = addService(&tService{Name:"test", Addr:"localhost:8888", LoginPeriod:30})
      if err != nil { quit(err) }
   }
   sServiceStartFn = iFn
}

func GetServices() (aS []string) {
   sServicesDoor.RLock(); defer sServicesDoor.RUnlock()
   for aK, _ := range sServices {
      aS = append(aS, aK)
   }
   return aS
}

func GetData(iSvc string) *tService {
   sServicesDoor.RLock(); defer sServicesDoor.RUnlock()
   return sServices[iSvc]
}

type tService struct {
   Name string
   Description string
   LoginPeriod int // seconds
   Addr string // for Dial()
   Uid string
   Node string
}

func svcDir   (iSvc string) string { return kServiceDir + iSvc + "/"        }
func tempDir  (iSvc string) string { return kServiceDir + iSvc + "/temp/"   }
func threadDir(iSvc string) string { return kServiceDir + iSvc + "/thread/" }
func cfgFile  (iSvc string) string { return kServiceDir + iSvc + "/config"  }

func addService(iService *tService) error {
   var err error
   if len(iService.Name) < 4 || strings.HasSuffix(iService.Name, ".tmp") {
      return tError(fmt.Sprintf("AddService: name %s not valid", iService.Name))
   }
   sServicesDoor.Lock(); defer sServicesDoor.Unlock()
   if sServices[iService.Name] != nil {
      return tError(fmt.Sprintf("AddService: name %s already exists", iService.Name))
   }
   aTemp := iService.Name + ".tmp"
   defer os.RemoveAll(svcDir(aTemp))
   for _, aDir := range [...]string{tempDir(aTemp), threadDir(aTemp)} {
      err = os.MkdirAll(aDir, 0700)
      if err != nil { quit(err) }
   }
   err = writeJsonFile(cfgFile(aTemp), iService)
   if err != nil { quit(err) }

   err = syncDir(svcDir(aTemp))
   if err != nil { quit(err) }
   err = os.Rename(svcDir(aTemp), svcDir(iService.Name))
   if err != nil { quit(err) }

   sServices[iService.Name] = iService
   if sServiceStartFn != nil {
      sServiceStartFn(iService.Name)
   }
   return nil
}

func updateService(iService *tService) error {
   var err error
   sServicesDoor.Lock(); defer sServicesDoor.Unlock()
   if sServices[iService.Name] == nil {
      return tError(fmt.Sprintf("UpdateService: %s not found", iService.Name))
   }
   err = storeFile(cfgFile(iService.Name), iService)
   if err != nil { quit(err) }

   sServices[iService.Name] = iService
   return nil
}

func GetQueue(iSvc string) ([]*SendRecord, error) {
   return nil, nil
}

func HandleMsg(iSvc string, iHead *Header, iData []byte, iR io.Reader) (Msg, func(*ClientState)) {
   var aFn func(*ClientState)
   var aMsgId string
   switch iHead.Op {
   case "registered":
      aNewSvc := *GetData(iSvc)
      aNewSvc.Uid = iHead.Uid
      aNewSvc.Node = iHead.NodeId
      err := updateService(&aNewSvc)
      if err != nil { return Msg{"op":iHead.Op, "err":err.Error()}, nil }
   case "delivery":
      storeReceived(iSvc, iHead, iData, iR)
      if iHead.SubHead.ThreadId == "" { // temp
         aFn = func(c *ClientState) { c.addThread(iHead.Id, iHead.Id) }
      } else {
         aFn = func(c *ClientState) {
            if c.getThread() == iHead.SubHead.ThreadId { c.openMsg(iHead.Id, true) }
         }
      }
      aMsgId = iHead.Id
   case "ack":
      if iHead.Id == "22" { break }
      storeSaved(iSvc, iHead)
      if iHead.Id[0] == '_' {
         aFn = func(c *ClientState) { c.renameThread(iHead.Id, iHead.MsgId) }
      } else {
         aTid := parseSaveId(iHead.Id).tid()
         aFn = func(c *ClientState) { c.renameMsg(aTid, iHead.Id, iHead.MsgId) }
      }
      aMsgId = iHead.MsgId
   }
   return Msg{"op":iHead.Op, "id":aMsgId}, aFn
}

func HandleUpdt(iSvc string, iState *ClientState, iUpdt *Update) (Msg, *SendRecord, func(*ClientState)) {
   switch iUpdt.Op {
   case "service_add":
      err := addService(iUpdt.Service)
      if err != nil {
         return Msg{"op":iUpdt.Op, "err":err.Error()}, nil, nil
      }
   case "service_update":
      err := updateService(iUpdt.Service)
      if err != nil {
         return Msg{"op":iUpdt.Op, "err":err.Error()}, nil, nil
      }
   case "thread_ohi":
      aTid := iState.getThread()
      if len(aTid) > 0 && aTid[0] == '_' { break }
      aData, _ := json.Marshal(Msg{"ThreadId":aTid})
      aHeadLen := len(aData)
      aData = append(aData, "ohi there"...)
      aSrec := &SendRecord{Head: Msg{"Op":7, "Id":"22", "DataLen":len(aData), "DataHead":aHeadLen,
                           "For":[]Msg{{"Id":GetData(iSvc).Uid, "Type":1}} }, Data:aData}
      return Msg{"op":iUpdt.Op}, aSrec, nil
   case "thread_set":
      aLastId := loadThread(iSvc, iUpdt.Thread.Id)
      iState.addThread(iUpdt.Thread.Id, aLastId)
   case "thread_save":
      if iUpdt.Thread.Id == "" {
         aTid := ""; if !iUpdt.Thread.New { aTid = iState.getThread() }
         iUpdt.Thread.Id = makeSaveId(aTid)
      }
      writeSaved(iSvc, iUpdt)
      if iUpdt.Thread.New {
         iState.addThread(iUpdt.Thread.Id, iUpdt.Thread.Id)
      } else {
         iState.openMsg(iUpdt.Thread.Id, true)
      }
      return Msg{"op":iUpdt.Op, "id":iUpdt.Thread.Id}, nil, nil
   case "thread_discard":
      deleteSaved(iSvc, iUpdt)
      var aFn func(*ClientState)
      if iUpdt.Thread.Id[0] == '_' {
         aTid := iState.getThread()
         aFn = func(c *ClientState) { c.discardThread(aTid) }
      } else {
         aFn = func(c *ClientState) { c.openMsg(iUpdt.Thread.Id, false) }
      }
      return Msg{"op":iUpdt.Op}, nil, aFn
   case "thread_send":
      if iUpdt.Thread.Id == "" { break }
      aSrec := &SendRecord{Head: Msg{"Op":7, "Id":iUpdt.Thread.Id, "DataLen":1,
                  "For":[]Msg{{"Id":"LG3KCJGZPVVNDPV6%JRK4H6FC6LS8P37", "Type":1}} }, Data:[]byte{'1'}}
      return Msg{"op":iUpdt.Op}, aSrec, nil
   case "thread_close":
      iState.openMsg(iUpdt.Thread.Id, false)
   case "history":
      iState.goThread(iUpdt.Navigate.History)
   case "tab_add":
      iState.addTab(iUpdt.Tab.Type, iUpdt.Tab.Term)
   case "tab_drop":
      iState.dropTab(iUpdt.Tab.Type)
   case "tab_select":
      iState.setTab(iUpdt.Tab.Type, iUpdt.Tab.PosFor, iUpdt.Tab.Pos)
   default:
      return Msg{"op":iUpdt.Op, "err":"unknown op"}, nil, nil
   }
   return Msg{"op":iUpdt.Op}, nil, nil
}

func Upload(iId string, iR io.Reader, iLen int64) error {
   aFd, err := os.OpenFile(UploadDir+iId, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
   if err != nil { return err }
   defer aFd.Close()
   _, err = io.CopyN(aFd, iR, iLen)
   if err != nil { return err }
   err = aFd.Sync()
   if err != nil { return err }
   err = syncDir(UploadDir)
   return err
}

func readDirNames(iPath string) ([]string, error) {
   aFd, err := os.Open(iPath)
   if err != nil { return nil, err }
   aList, err := aFd.Readdirnames(0)
   aFd.Close()
   return aList, err
}

func storeFile(iPath string, iData interface{}) error {
   aTemp := iPath + ".tmp"
   defer os.Remove(aTemp)
   err := writeJsonFile(aTemp, iData)
   if err != nil { return err }
   err = syncDir(path.Dir(iPath))
   if err != nil { return err }
   err = os.Remove(iPath)
   if err != nil && !os.IsNotExist(err) { return err }
   err = os.Rename(aTemp, iPath)
   if err != nil {
      fmt.Fprintf(os.Stderr, "transaction failed...")
      quit(err)
   }
   return nil
}

func writeJsonFile(iPath string, iData interface{}) error {
   aFd, err := os.OpenFile(iPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
   if err != nil { return err }
   defer aFd.Close()
   err = json.NewEncoder(aFd).Encode(iData)
   if err != nil { return err }
   err = aFd.Sync()
   return err
}

func resolveTmpFile(iPath string) error {
   err := os.Rename(iPath, iPath[:len(iPath)-4])
   if err != nil {
      if os.IsNotExist(err) {
         err = nil
      } else if os.IsExist(err) {
         err = os.Remove(iPath)
      }
   }
   return err
}

func syncDir(iPath string) error {
   aFd, err := os.Open(iPath)
   if err != nil { return err }
   err = aFd.Sync()
   aFd.Close()
   return err
}

func quit(err error) {
   fmt.Fprintf(os.Stderr, "quit after %s\n", err.Error())
   debug.PrintStack()
   os.Exit(3)
}

type tError string
func (o tError) Error() string { return string(o) }
