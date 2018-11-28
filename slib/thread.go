// Copyright 2017 Liam Breck
//
// This file is part of the "mnm" software. Anyone may redistribute mnm and/or modify
// it under the terms of the GNU Lesser General Public License version 3, as published
// by the Free Software Foundation. See www.gnu.org/licenses/
// Mnm is distributed WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See said License for details.

package slib

import (
   "bytes"
   "hash/crc32"
   "fmt"
   "io"
   "io/ioutil"
   "encoding/json"
   "os"
   "path"
   "sort"
   "strconv"
   "strings"
   "sync"
)

const kCcNoteMaxLen = 1024

type tIndexEl struct {
   Id string
   Offset, Size int64
   From string
   Alias string
   Date string
   Subject string
   Checksum uint32
   Seen string // mutable
}

const eSeenClear, eSeenLocal string = "!", "."

func _setupIndexEl(iEl *tIndexEl, iHead *Header, iPos int64) *tIndexEl {
   iEl.Id, iEl.From, iEl.Date, iEl.Offset, iEl.Subject, iEl.Alias =
      iHead.Id, iHead.From, iHead.Posted, iPos, iHead.SubHead.Subject, iHead.SubHead.Alias
   return iEl
}

type tCcEl struct {
   tCcElCore
   Checksum uint32 `json:",omitempty"`
}

type tCcElCore struct {
   Who, By string
   WhoUid, ByUid string
   Date string
   Note string
   Subscribe bool
}

type tFwdEl struct {
   Id string
   Cc []tCcEl
}

func GetIdxThread(iSvc string, iState *ClientState) interface{} {
   aIdx := []struct{ Id, From, Alias, Date, Subject, Seen string
                     Queued bool }{}
   aTid := iState.getThread()
   if aTid == "" { return aIdx }
   func() {
      cDoor := _getThreadDoor(iSvc, aTid)
      cDoor.RLock(); defer cDoor.RUnlock()
      if cDoor.renamed { return }

      cFd, err := os.Open(threadDir(iSvc) + aTid)
      if err != nil { quit(err) }
      defer cFd.Close()
      _ = _readIndex(cFd, &aIdx, nil)
   }()
   for a, _ := range aIdx {
      if aIdx[a].From == "" {
         aIdx[a].Queued = hasQueue(iSvc, eSrecThread, aIdx[a].Id)
      }
   }
   for a1, a2 := 0, len(aIdx)-1; a1 < a2; a1, a2 = a1+1, a2-1 {
      aIdx[a1], aIdx[a2] = aIdx[a2], aIdx[a1]
   }
   return aIdx
}

func GetCcThread(iSvc string, iState *ClientState) interface{} {
   type tCcElFwd struct {
      tCcElCore
      Queued bool
      Qid string `json:",omitempty"`
   }
   const kDraft, kSet = 0, 1
   aCc := [2][]tCcElFwd{{},{}}
   aTid := iState.getThread()
   if aTid == "" { return aCc }

   aDoor := _getThreadDoor(iSvc, aTid)
   aDoor.RLock()
   if aDoor.renamed {
      aDoor.RUnlock()
      return aCc
   }
   aFd, err := os.Open(threadDir(iSvc) + aTid)
   if err != nil { quit(err) }
   _readCc(aFd, &aCc[kSet])
   aFd.Close(); aDoor.RUnlock()

   aDoor = _getThreadDoor(iSvc, aTid + "_forward")
   aDoor.RLock()
   aFwd := _getFwd(iSvc, aTid, "")
   aDoor.RUnlock()
   for a := range aFwd {
      aN := kDraft; if hasQueue(iSvc, eSrecFwd, aFwd[a].Id) { aN = kSet }
      aQid := ""; if aN == kDraft { aQid = aFwd[a].Id }
      for a1 := range aFwd[a].Cc {
         aCc[aN] = append(aCc[aN], tCcElFwd{tCcElCore:aFwd[a].Cc[a1].tCcElCore, Queued:aN==kSet, Qid:aQid})
      }
   }

   sort.Slice(aCc[kSet], func(cA, cB int) bool { return aCc[kSet][cA].Who < aCc[kSet][cB].Who })
   return aCc
}

func WriteMessagesThread(iW io.Writer, iSvc string, iState *ClientState, iId string) error {
   aTid := iState.getThread()
   if aTid == "" { return nil }

   aDoor := _getThreadDoor(iSvc, aTid)
   aDoor.RLock(); defer aDoor.RUnlock()
   if aDoor.renamed { return tError("thread name changed") }

   aFd, err := os.Open(threadDir(iSvc) + aTid)
   if err != nil { quit(err) }
   defer aFd.Close()
   var aIdx []tIndexEl
   _ = _readIndex(aFd, &aIdx, nil)
   for a, _ := range aIdx {
      if iId != "" && aIdx[a].Id == iId || iId == "" && iState.isOpen(aIdx[a].Id) {
         var aRd, aXd *os.File
         if aIdx[a].Offset >= 0 {
            _, err = aFd.Seek(aIdx[a].Offset, io.SeekStart)
            if err != nil { quit(err) }
            aXd = aFd
         } else {
            aRd, err = os.Open(threadDir(iSvc) + aIdx[a].Id)
            if err != nil { quit(err) }
            defer aRd.Close()
            aXd = aRd
         }
         _, err = io.CopyN(iW, aXd, aIdx[a].Size)
         if err != nil { return err } //todo only return network errors
         if iId != "" {
            break
         }
      }
   }
   return nil
}

func sendDraftThread(iW io.Writer, iSvc string, iDraftId, iId string) error {
   aFd, err := os.Open(threadDir(iSvc) + iDraftId)
   if err != nil {
      if !os.IsNotExist(err) { quit(err) }
      fmt.Fprintf(os.Stderr, "sendDraftThread %s: draft file was cleared %s\n", iSvc, iDraftId)
      return tError("already sent")
   }
   defer aFd.Close()

   aId := parseLocalId(iDraftId)
   aDh := _readDraftHead(aFd)
   aCc := aDh.SubHead.Cc
   if aCc == nil {
      aDoor := _getThreadDoor(iSvc, aId.tid())
      aDoor.RLock()
      var aOfd *os.File
      aOfd, err = os.Open(threadDir(iSvc) + aId.tid())
      if err != nil { quit(err) }
      _readCc(aOfd, &aCc)
      aOfd.Close(); aDoor.RUnlock()
   }

   aAttachLen := sizeDraftAttach(iSvc, &aDh.SubHead, aId) // revs subhead
   aBuf1, err := json.Marshal(aDh.SubHead)
   if err != nil { quit(err) }
   aUid := GetConfigService(iSvc).Uid
   aFor := make([]tHeaderFor, len(aCc)-1)
   for a,aC := 0,0; a < len(aFor); a++ {
      if aCc[aC].WhoUid == aUid { aC++ }
      aType := eForUser; if aCc[aC].WhoUid == aCc[aC].Who { aType = eForGroupExcl }
      aFor[a].Id, aFor[a].Type = aCc[aC].WhoUid, aType
      aC++
   }
   aHead := Msg{"Op":7, "Id":iId, "For":aFor,
                "DataHead": len(aBuf1), "DataLen": int64(len(aBuf1)) + aDh.Len + aAttachLen }
   aBuf0, err := json.Marshal(aHead)
   if err != nil { quit(err) }

   err = sendHeaders(iW, aBuf0, aBuf1)
   if err != nil { return err }
   _, err = io.CopyN(iW, aFd, aDh.Len) //todo only return network errors
   if err != nil { return err }
   err = sendDraftAttach(iW, iSvc, &aDh.SubHead, aId, aFd)
   return err
}

//todo return open-msg map
func loadThread(iSvc string, iId string) string {
   aDoor := _getThreadDoor(iSvc, iId)
   aDoor.RLock(); defer aDoor.RUnlock()
   if aDoor.renamed { return "" }

   aFd, err := os.Open(threadDir(iSvc) + iId)
   if err != nil { quit(err) }
   defer aFd.Close()
   var aIdx []tIndexEl
   _ = _readIndex(aFd, &aIdx, nil)
   for a := len(aIdx)-1; a >= 0; a-- {
      if aIdx[a].Seen != "" {
         return aIdx[a].Id
      }
   }
   return aIdx[0].Id
}

func storeReceivedThread(iSvc string, iHead *Header, iR io.Reader) error {
   var err error
   aThreadId := iHead.SubHead.ThreadId; if aThreadId == "" { aThreadId = iHead.Id }
   aOrig := threadDir(iSvc) + aThreadId
   aTempOk := tempDir(iSvc) + aThreadId + "_" + iHead.Id + "_sr__"
   aTemp := aTempOk + ".tmp"

   fConsume := func() error {
      _, err = io.CopyN(ioutil.Discard, iR, iHead.DataLen)
      return err
   }
   if iHead.SubHead.ThreadId == "" {
      _, err = os.Lstat(aOrig)
      if err == nil {
         fmt.Fprintf(os.Stderr, "storeReceivedThread %s: thread %s already stored\n", iSvc, iHead.Id)
         return fConsume()
      }
   } else if iHead.SubHead.ThreadId[0] == '_' {
      fmt.Fprintf(os.Stderr, "storeReceivedThread %s: invalid thread id %s\n", iSvc, iHead.SubHead.ThreadId)
      return fConsume()
   }

   var aTd, aFd *os.File
   aIdx, aCc := []tIndexEl{{}}, []tCcEl{}
   aIdxN := 0
   var aPos, aCopyLen int64
   aEl := tIndexEl{Seen:eSeenClear}
   aHeadCc := iHead.SubHead.Cc; if aThreadId != iHead.Id { aHeadCc = nil }
   iHead.SubHead.Cc = nil

   aTd, err = os.OpenFile(aTemp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
   if err != nil { quit(err) }
   defer aTd.Close()
   err = _writeMsgTemp(aTd, iHead, iR, &aEl)
   if err == nil {
      err = tempReceivedAttach(iSvc, iHead, iR)
   }
   if err != nil {
      os.Remove(aTemp)
      return err
   }
   if aThreadId == iHead.Id {
      if aHeadCc != nil { //todo handle invalid/missing SubHead.Cc
         aCc = aHeadCc
         _revCc(aCc, iHead)
      }
   } else {
      aDoor := _getThreadDoor(iSvc, aThreadId)
      aDoor.Lock(); defer aDoor.Unlock()
      aFd, err = os.OpenFile(aOrig, os.O_RDWR, 0600)
      if err != nil {
         fmt.Fprintf(os.Stderr, "storeReceivedThread %s: thread %s not found\n", iSvc, aThreadId)
         os.Remove(aTemp)
         return nil
      }
      defer aFd.Close()
      aPos = _readIndex(aFd, &aIdx, &aCc)
      aIdxN = len(aIdx)
      aIdx = append(aIdx, tIndexEl{})
      for a, _ := range aIdx {
         if aIdx[a].Id <  iHead.Id || aIdx[a].Offset < 0 { continue }
         if aIdx[a].Id == iHead.Id {
            fmt.Fprintf(os.Stderr, "storeReceivedThread %s: msg %s already stored\n", iSvc, iHead.Id)
            os.Remove(aTemp)
            return nil
         }
         if aCopyLen == 0 {
            aCopyLen = aPos - aIdx[a].Offset
            aPos = aIdx[a].Offset
            aIdxN = a
            copy(aIdx[a+1:], aIdx[a:])
            _, err = aFd.Seek(aPos, io.SeekStart)
            if err != nil { quit(err) }
            _, err = io.CopyN(aTd, aFd, aCopyLen)
            if err != nil { quit(err) }
            _, err = aFd.Seek(aPos, io.SeekStart)
            if err != nil { quit(err) }
         } else {
            aIdx[a].Offset += aEl.Size
         }
      }
   }
   aIdx[aIdxN] = *_setupIndexEl(&aEl, iHead, aPos)
   aTempOk += fmt.Sprint(aPos)

   _writeIndex(aTd, aIdx, aCc)
   err = os.Rename(aTemp, aTempOk)
   if err != nil { quit(err) }
   err = syncDir(tempDir(iSvc))
   if err != nil { quit(err) }
   _completeStoreReceived(iSvc, path.Base(aTempOk), _makeDraftHead(iHead, aHeadCc), aFd, aTd)
   return nil
}

func _completeStoreReceived(iSvc string, iTmp string, iHead *tDraftHead, iFd, iTd *os.File) {
   var err error
   aRec := _parseTempOk(iTmp)
   aTempOk := tempDir(iSvc) + iTmp

   resolveSentAdrsbk    (iSvc, iHead.Posted, iHead.cc, aRec.tid())
   resolveReceivedAdrsbk(iSvc, iHead.Posted, iHead.cc, aRec.tid())
   storeReceivedAttach(iSvc, &iHead.SubHead, aRec)

   if aRec.tid() == aRec.mid() {
      err = os.Link(aTempOk, threadDir(iSvc) + aRec.tid())
      if err != nil && !os.IsExist(err) { quit(err) }
      err = syncDir(threadDir(iSvc))
      if err != nil { quit(err) }
   } else {
      _, err = io.Copy(iFd, iTd) // iFd has correct pos from _readIndex
      if err != nil { quit(err) }
      err = iFd.Sync()
      if err != nil { quit(err) }
   }
   err = os.Remove(aTempOk)
   if err != nil { quit(err) }
}

func seenReceivedThread(iSvc string, iUpdt *Update) {
   aOrig := threadDir(iSvc) + iUpdt.Thread.ThreadId
   aTempOk := tempDir(iSvc) + iUpdt.Thread.ThreadId + "__nr__"
   aTemp := aTempOk + ".tmp"
   var err error

   aDoor := _getThreadDoor(iSvc, iUpdt.Thread.ThreadId)
   aDoor.Lock(); defer aDoor.Unlock()
   if aDoor.renamed { return }

   var aTd, aFd *os.File
   aIdx, aCc := []tIndexEl{}, []tCcEl{}
   aIdxN := -1
   var aPos int64

   aFd, err = os.OpenFile(aOrig, os.O_RDWR, 0600)
   if err != nil { quit(err) }
   defer aFd.Close()
   aPos = _readIndex(aFd, &aIdx, &aCc)
   for aIdxN, _ = range aIdx {
      if aIdx[aIdxN].Id == iUpdt.Thread.Id { break }
   }
   if aIdx[aIdxN].Seen != "" {
      return
   }
   aIdx[aIdxN].Seen = dateRFC3339()
   aTempOk += fmt.Sprint(aPos)

   aTd, err = os.OpenFile(aTemp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
   if err != nil { quit(err) }
   defer aTd.Close()
   _writeIndex(aTd, aIdx, aCc)
   err = os.Rename(aTemp, aTempOk)
   if err != nil { quit(err) }
   err = syncDir(tempDir(iSvc))
   if err != nil { quit(err) }
   _completeSeenReceived(iSvc, path.Base(aTempOk), aFd, aTd)
}

func _completeSeenReceived(iSvc string, iTmp string, iFd, iTd *os.File) {
   var err error
   aTempOk := tempDir(iSvc) + iTmp

   _, err = io.Copy(iFd, iTd) // iFd has correct pos from _readIndex
   if err != nil { quit(err) }
   err = iFd.Sync()
   if err != nil { quit(err) }
   err = os.Remove(aTempOk)
   if err != nil { quit(err) }
}

func storeSentThread(iSvc string, iHead *Header) {
   var err error
   aId := parseLocalId(iHead.Id)
   if aId.tid() == "" {
      aId.tidSet(iHead.MsgId)
   }
   aDraft := threadDir(iSvc) + iHead.Id
   aOrig := threadDir(iSvc) + aId.tid()
   aTempOk := tempDir(iSvc) + aId.tid() + "_" + iHead.MsgId + "_ss_" + aId.lms() + "_"
   aTemp := aTempOk + ".tmp"

   aSd, err := os.Open(aDraft)
   if err != nil {
      if !os.IsNotExist(err) { quit(err) }
      fmt.Fprintf(os.Stderr, "storeSentThread %s: draft file was cleared %s\n", iSvc, iHead.Id)
      return
   }
   defer aSd.Close()
   aDh := _readDraftHead(aSd)

   aTid := iHead.Id; if aId.tid() != iHead.MsgId { aTid = aId.tid() }
   aDoor := _getThreadDoor(iSvc, aTid)
   aDoor.Lock(); defer aDoor.Unlock()
   if aDoor.renamed { quit(tError("unexpected rename")) }
   if aId.tid() == iHead.MsgId {
      aDoor.renamed = true
   }

   var aTd, aFd *os.File
   aIdx, aCc := []tIndexEl{}, []tCcEl{}
   var aPos int64
   aEl := tIndexEl{}
   aHeadCc := aDh.SubHead.Cc
   aDh.SubHead.Cc = nil

   if aId.tid() == iHead.MsgId {
      aCc = aHeadCc
      _revCc(aCc, iHead)
   } else {
      aFd, err = os.OpenFile(aOrig, os.O_RDWR, 0600)
      if err != nil { quit(err) }
      defer aFd.Close()
      aPos = _readIndex(aFd, &aIdx, &aCc)
      a := -1
      for a, _ = range aIdx {
         if aIdx[a].Id == iHead.Id { break }
      }
      aIdx = aIdx[:a + copy(aIdx[a:], aIdx[a+1:])]
   }
   aHead := Header{Id:iHead.MsgId, From:GetConfigService(iSvc).Uid, Posted:iHead.Posted,
                   DataLen:aDh.Len, SubHead:aDh.SubHead}
   aHead.SubHead.setupSent(aId.tid())
   aIdx = append(aIdx, *_setupIndexEl(&aEl, &aHead, aPos))
   aTempOk += fmt.Sprint(aPos)

   aTd, err = os.OpenFile(aTemp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
   if err != nil { quit(err) }
   defer aTd.Close()
   _writeMsgTemp(aTd, &aHead, aSd, &aIdx[len(aIdx)-1])
   _writeIndex(aTd, aIdx, aCc)
   tempSentAttach(iSvc, &aHead, aSd)
   err = os.Rename(aTemp, aTempOk)
   if err != nil { quit(err) }
   err = syncDir(tempDir(iSvc))
   if err != nil { quit(err) }
   _completeStoreSent(iSvc, path.Base(aTempOk), _makeDraftHead(&aHead, aHeadCc), aFd, aTd)
}

func _completeStoreSent(iSvc string, iTmp string, iHead *tDraftHead, iFd, iTd *os.File) {
   aRec := _parseTempOk(iTmp)

   resolveReceivedAdrsbk(iSvc, iHead.Posted, iHead.cc, aRec.tid())
   storeSentAttach(iSvc, &iHead.SubHead, aRec)

   aTid := ""; if aRec.tid() != aRec.mid() { aTid = aRec.tid() }
   err := os.Remove(threadDir(iSvc) + aTid + "_" + aRec.lms())
   if err != nil && !os.IsNotExist(err) { quit(err) }

   _completeStoreReceived(iSvc, iTmp, &tDraftHead{}, iFd, iTd)
}

func validateDraftThread(iSvc string, iUpdt *Update) error {
   aId := parseLocalId(iUpdt.Thread.Id)
   aFd, err := os.Open(threadDir(iSvc) + aId.tid() + "_" + aId.lms())
   if err != nil { quit(err) }
   defer aFd.Close()
   aDh := _readDraftHead(aFd)
   if aDh.SubHead.Subject == "" && aId.tid() == "" {
      return tError("subject missing")
   }
   _, err = aFd.Seek(aDh.Len, io.SeekCurrent)
   if err != nil { quit(err) }
   err = validateDraftAttach(iSvc, &aDh.SubHead, aId, aFd)
   return err
}

func storeDraftThread(iSvc string, iUpdt *Update) {
   aId := parseLocalId(iUpdt.Thread.Id)
   aOrig := threadDir(iSvc) + aId.tid()
   aTempOk := tempDir(iSvc) + aId.tid() + "__ws_" + aId.lms() + "_"
   aTemp := aTempOk + ".tmp"
   aData := bytes.NewBufferString(iUpdt.Thread.Data)
   var err error

   aTid := aId.tid(); if aTid == "" { aTid = "_" + aId.lms() }
   aDoor := _getThreadDoor(iSvc, aTid)
   aDoor.Lock(); defer aDoor.Unlock()
   if aDoor.renamed { quit(tError("unexpected rename")) }

   var aTd, aFd *os.File
   aIdx, aCc := []tIndexEl{}, []tCcEl{}
   aIdxN := -1
   var aPos int64
   aEl := tIndexEl{Id:iUpdt.Thread.Id, Date:dateRFC3339(), Subject:iUpdt.Thread.Subject, Offset:-1}

   if aId.tid() == "" {
      aCc = _updateCc(iSvc, iUpdt.Thread.Cc, false)
      iUpdt.Thread.Cc = aCc
   } else {
      aFd, err = os.OpenFile(aOrig, os.O_RDWR, 0600)
      if err != nil { quit(err) }
      defer aFd.Close()
      aPos = _readIndex(aFd, &aIdx, &aCc)
      for a, _ := range aIdx {
         if aIdx[a].Id == iUpdt.Thread.Id {
            aIdx[a] = aEl
            aIdxN = a
            break
         }
      }
   }
   if aIdxN == -1 {
      aIdx = append(aIdx, aEl)
      aIdxN = len(aIdx)-1
   }
   aTempOk += fmt.Sprint(aPos)

   aTd, err = os.OpenFile(aTemp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
   if err != nil { quit(err) }
   defer aTd.Close()
   aHead := Header{Id:iUpdt.Thread.Id, From:"self", Posted:"draft", DataLen:int64(aData.Len())}
   aHead.SubHead.setupDraft(aId.tid(), iUpdt, iSvc)
   _writeMsgTemp(aTd, &aHead, aData, &aIdx[aIdxN]) //todo stream from client
   writeFormFillAttach(aTd, &aHead.SubHead, iUpdt.Thread.FormFill, &aIdx[aIdxN])
   _writeIndex(aTd, aIdx, aCc)
   err = os.Rename(aTemp, aTempOk)
   if err != nil { quit(err) }
   err = syncDir(tempDir(iSvc))
   if err != nil { quit(err) }
   _completeStoreDraft(iSvc, path.Base(aTempOk), _makeDraftHead(&aHead, nil), aFd, aTd)
}

func _completeStoreDraft(iSvc string, iTmp string, iHead *tDraftHead, iFd, iTd *os.File) {
   var err error
   aRec := _parseTempOk(iTmp)
   aDraft := threadDir(iSvc) + aRec.tid() + "_" + aRec.lms()
   aTempOk := tempDir(iSvc) + iTmp

   var aSubHeadOld *tHeader2
   aSd, err := os.Open(aDraft)
   if err != nil {
      if !os.IsNotExist(err) { quit(err) }
   } else {
      aSubHeadOld = &_readDraftHead(aSd).SubHead
      aSd.Close()
   }
   updateDraftAttach(iSvc, aSubHeadOld, &iHead.SubHead, aRec)

   err = os.Remove(aDraft)
   if err != nil && !os.IsNotExist(err) { quit(err) }
   if aRec.op() == "ws" {
      err = os.Link(aTempOk, aDraft)
      if err != nil { quit(err) }
   }
   err = syncDir(threadDir(iSvc))
   if err != nil { quit(err) }

   if aRec.tid() != "" {
      _ = _readIndex(iTd, nil, nil)
      err = iFd.Truncate(aRec.pos())
      if err != nil { quit(err) }
      _, err = io.Copy(iFd, iTd)
      if err != nil { quit(err) }
      err = iFd.Sync()
      if err != nil { quit(err) }
   }
   err = os.Remove(aTempOk)
   if err != nil { quit(err) }
}

func deleteDraftThread(iSvc string, iUpdt *Update) {
   aId := parseLocalId(iUpdt.Thread.Id)
   aOrig := threadDir(iSvc) + aId.tid()
   aTempOk := tempDir(iSvc) + aId.tid() + "__ds_" + aId.lms() + "_"
   aTemp := aTempOk + ".tmp"
   var err error

   aTid := aId.tid(); if aTid == "" { aTid = "_" + aId.lms() }
   aDoor := _getThreadDoor(iSvc, aTid)
   aDoor.Lock(); defer aDoor.Unlock()
   if aDoor.renamed { quit(tError("unexpected rename")) }

   var aTd, aFd *os.File
   aIdx, aCc := []tIndexEl{}, []tCcEl{}
   var aPos int64

   if aId.tid() != "" {
      aFd, err = os.OpenFile(aOrig, os.O_RDWR, 0600)
      if err != nil { quit(err) }
      defer aFd.Close()
      aPos = _readIndex(aFd, &aIdx, &aCc)
      a := -1
      for a, _ = range aIdx {
         if aIdx[a].Id == iUpdt.Thread.Id { break }
      }
      if aIdx[a].Id != iUpdt.Thread.Id {
         return
      }
      aIdx = aIdx[:a + copy(aIdx[a:], aIdx[a+1:])]
   }
   aTempOk += fmt.Sprint(aPos)

   aTd, err = os.OpenFile(aTemp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
   if err != nil { quit(err) }
   defer aTd.Close()
   _writeIndex(aTd, aIdx, aCc)
   err = os.Rename(aTemp, aTempOk)
   if err != nil { quit(err) }
   err = syncDir(tempDir(iSvc))
   if err != nil { quit(err) }
   _completeDeleteDraft(iSvc, path.Base(aTempOk), aFd, aTd)
}

func _completeDeleteDraft(iSvc string, iTmp string, iFd, iTd *os.File) {
   _completeStoreDraft(iSvc, iTmp, &tDraftHead{}, iFd, iTd)
}

func storeFwdDraftThread(iSvc string, iUpdt *Update) {
   aFwdOrig := fwdFile(iSvc, iUpdt.Forward.ThreadId)
   aFwdTemp := tempDir(iSvc) + "forward_" + iUpdt.Forward.ThreadId

   if iUpdt.Forward.ThreadId[0] == '_' { quit(tError("cannot forward draft")) }

   aCcNew := iUpdt.Forward.Cc
   fCheckInput := func(cSet []tCcEl) {
      for c := len(aCcNew)-1; c >= 0; c-- {
         if aCcNew[c].Date != "" { continue }
         for c1 := range cSet {
            if cSet[c1].WhoUid == aCcNew[c].WhoUid {
               aCcNew = aCcNew[:c + copy(aCcNew[c:], aCcNew[c+1:])]
               break
            }
         }
      }
   }
   var aCcOrig []tCcEl
   aDoor := _getThreadDoor(iSvc, iUpdt.Forward.ThreadId)
   aDoor.RLock()
   aFd, err := os.Open(threadDir(iSvc) + iUpdt.Forward.ThreadId)
   if err != nil { quit(err) }
   _readCc(aFd, &aCcOrig)
   aFd.Close(); aDoor.RUnlock()
   fCheckInput(aCcOrig)

   aDoor = _getThreadDoor(iSvc, iUpdt.Forward.ThreadId + "_forward")
   aDoor.Lock(); defer aDoor.Unlock()

   aFwd := _getFwd(iSvc, iUpdt.Forward.ThreadId, "make")
   if len(aFwd) == 0 || hasQueue(iSvc, eSrecFwd, aFwd[len(aFwd)-1].Id) {
      aFwd = append(aFwd, tFwdEl{Id:makeLocalId(iUpdt.Forward.ThreadId)})
   }
   for a := 0; a < len(aFwd)-1; a++ {
      fCheckInput(aFwd[a].Cc)
   }
   if len(aFwd) == 1 && len(aCcNew) == 0 {
      err = os.Remove(aFwdOrig)
      return
   }
   aFwd[len(aFwd)-1].Cc = _updateCc(iSvc, aCcNew, true)
   err = writeJsonFile(aFwdTemp, aFwd)
   if err != nil { quit(err) }
   err = syncDir(tempDir(iSvc))
   if err != nil { quit(err) }
   err = os.Remove(aFwdOrig)
   if err != nil { quit(err) }
   err = os.Rename(aFwdTemp, aFwdOrig)
   if err != nil { quit(err) }
}

func _getFwd(iSvc string, iTid string, iOpt string) []tFwdEl {
   aPath := fwdFile(iSvc, iTid)
   if iOpt == "temp" {
      aPath = tempDir(iSvc) + iTid + "_forward.tmp"
      iOpt = ""
   }
   var aFwd []tFwdEl
   aFd, err := os.Open(aPath)
   if err != nil {
      if iOpt == "exist" || !os.IsNotExist(err) { quit(err) }
      if iOpt == "" {
         return aFwd
      }
      if iOpt != "make" { quit(tError("unknown option "+iOpt)) }
      _, err = os.Lstat(threadDir(iSvc) + iTid)
      if err != nil { quit(err) }
      err = os.Symlink("placeholder", aPath)
      if err == nil {
         err = syncDir(threadDir(iSvc))
      }
      if err != nil && !os.IsExist(err) { quit(err) }
   } else {
      err = json.NewDecoder(aFd).Decode(&aFwd)
      aFd.Close()
      if err != nil { quit(err) }
   }
   return aFwd
}

func _updateCc(iSvc string, iCc []tCcEl, iOmitSelf bool) []tCcEl {
   aCfg := GetConfigService(iSvc)
   for a := range iCc {
      iOmitSelf = iOmitSelf || iCc[a].WhoUid == aCfg.Uid
      if iCc[a].Date != "" { continue }
      iCc[a].Date = "."
      iCc[a].ByUid = aCfg.Uid
      iCc[a].By = aCfg.Alias
      iCc[a].Subscribe = true
   }
   if !iOmitSelf {
      iCc = append(iCc, tCcEl{tCcElCore:tCcElCore{
                              Who: aCfg.Alias, WhoUid: aCfg.Uid,
                              By:  aCfg.Alias, ByUid:  aCfg.Uid,
                              Date: ".", Note: "author", Subscribe: true}})
   }
   return iCc
}

func _revCc(iCc []tCcEl, iHead *Header) {
   var err error
   var aBuf []byte
   for a := range iCc {
      if len(iCc[a].Note) > kCcNoteMaxLen {
         iCc[a].Note = iCc[a].Note[:kCcNoteMaxLen-7] + "[trunc]"
      }
      iCc[a].Date = iHead.Posted
      aBuf, err = json.Marshal(iCc[a])
      if err != nil { quit(err) }
      iCc[a].Checksum = crc32.Checksum(aBuf, sCrc32c)
   }
}

type tDraftHead struct {
   Len int64
   Posted string
   From string
   SubHead tHeader2
   cc []tCcEl
}

func _makeDraftHead(iHead *Header, iCc []tCcEl) *tDraftHead {
   return &tDraftHead{Posted:iHead.Posted, From:iHead.From, SubHead:iHead.SubHead, cc:iCc}
}

func _readDraftHead(iFd *os.File) *tDraftHead {
   var aHead tDraftHead
   aBuf := make([]byte, 65536)
   _, err := iFd.Read(aBuf[:4])
   if err != nil { quit(err) }
   aUi, _ := strconv.ParseUint(string(aBuf[:4]), 16, 0)
   _, err = iFd.Read(aBuf[:aUi])
   if err != nil { quit(err) }
   err = json.Unmarshal(aBuf[:aUi], &aHead)
   if err != nil { quit(err) }
   _, err = iFd.Seek(1, io.SeekCurrent) // consume newline
   if err != nil { quit(err) }
   return &aHead
}

func _parseTempOk(i string) tComplete { return strings.SplitN(i, "_", 5) }

type tComplete []string

func (o tComplete) tid() string { return o[0] } // thread id
func (o tComplete) mid() string { return o[1] } // message id
func (o tComplete)  op() string { return o[2] } // transaction type
func (o tComplete) lms() string { return o[3] } // local id milliseconds

func (o tComplete) pos() int64 { // thread offset to index
   aPos, err := strconv.ParseInt(o[4], 10, 64)
   if err != nil { quit(err) }
   return aPos
}

func _readIndex(iFd *os.File, iIdx, iCc interface{}) int64 {
   aLenIdx, aLenCc := _readTail(iFd)
   aPos, err := iFd.Seek(-16 - aLenIdx - aLenCc, io.SeekEnd)
   if err != nil { quit(err) }
   if iIdx == nil {
      return aPos
   }
   aBuf := make([]byte, aLenIdx)
   _, err = iFd.Read(aBuf) //todo ensure all read
   if err != nil { quit(err) }
   err = json.Unmarshal(aBuf, iIdx)
   if err != nil { quit(err) }
   if iCc != nil {
      aBuf = make([]byte, aLenCc)
      _, err = iFd.Read(aBuf) //todo ensure all read
      if err != nil { quit(err) }
      err = json.Unmarshal(aBuf, iCc)
      if err != nil { quit(err) }
   }
   _, err = iFd.Seek(aPos, io.SeekStart)
   if err != nil { quit(err) }
   return aPos
}

func _readCc(iFd *os.File, iCc interface{}) {
   _, aLenCc := _readTail(iFd)
   aBuf := make([]byte, aLenCc)
   _, err := iFd.Seek(-16 - aLenCc, io.SeekEnd)
   if err != nil { quit(err) }
   _, err = iFd.Read(aBuf) //todo ensure all read
   if err != nil { quit(err) }
   err = json.Unmarshal(aBuf, iCc)
   if err != nil { quit(err) }
}

func _readTail(iFd *os.File) (int64, int64) {
   aBuf := make([]byte, 16)
   _, err := iFd.Seek(-16, io.SeekEnd)
   if err != nil { quit(err) }
   _, err = iFd.Read(aBuf)
   if err != nil { quit(err) }
   aStr := string(aBuf)
   aLenIdx, err := strconv.ParseUint(aStr[:8], 16, 0)
   if err != nil { quit(err) }
   aLenCc,  err := strconv.ParseUint(aStr[8:], 16, 0)
   if err != nil { quit(err) }
   return int64(aLenIdx), int64(aLenCc)
}

func _writeIndex(iTd *os.File, iIdx []tIndexEl, iCc []tCcEl) {
   aBuf, err := json.Marshal(iIdx)
   if err != nil { quit(err) }
   _, err = iTd.Write(aBuf)
   if err != nil { quit(err) }
   _writeCc(iTd, iCc, len(aBuf))
}

func _writeCc(iTd *os.File, iCc []tCcEl, iLenIdx int) {
   aBuf, err := json.Marshal(iCc)
   if err != nil { quit(err) }
   _, err = iTd.Write(append(aBuf, fmt.Sprintf("%08x%08x", iLenIdx, len(aBuf))...))
   if err != nil { quit(err) }
   err = iTd.Sync()
   if err != nil { quit(err) }
   _, err = iTd.Seek(0, io.SeekStart)
   if err != nil { quit(err) }
}

func _writeMsgTemp(iTd *os.File, iHead *Header, iR io.Reader, iEl *tIndexEl) error {
   var err error
   var aCw tCrcWriter
   aTee := io.MultiWriter(iTd, &aCw)
   aSize := iHead.DataLen - totalAttach(&iHead.SubHead)
   if aSize < 0 { return tError("attachment size total exceeds DataLen") }
   aBuf, err := json.Marshal(Msg{"Id":iHead.Id, "From":iHead.From, "Posted":iHead.Posted,
                                 "Len":aSize, "SubHead":iHead.SubHead})
   if err != nil { quit(err) }
   aLen, err := aTee.Write([]byte(fmt.Sprintf("%04x", len(aBuf))))
   if err != nil { quit(err) }
   if aLen != 4 { quit(tError("json input too long")) }
   _, err = aTee.Write(append(aBuf, '\n'))
   if err != nil { quit(err) }
   if aSize > 0 {
      _, err = io.CopyN(aTee, iR, aSize)
      if err != nil { return err }
   }
   _, err = iTd.Write([]byte{'\n'})
   if err != nil { quit(err) }
   if iEl.Seen == eSeenClear {
      iEl.Seen = ""
   } else {
      iEl.Seen = eSeenLocal
   }
   iEl.Checksum = aCw.sum // excludes final '\n'
   iEl.Size, err = iTd.Seek(0, io.SeekCurrent)
   if err != nil { quit(err) }
   return nil
}

type tThreadDoor struct {
   sync.RWMutex
   renamed bool //todo provide new thread id here?
}

func _getThreadDoor(iSvc string, iTid string) *tThreadDoor {
   return getDoorService(iSvc, iTid, func()tDoor{ return &tThreadDoor{} }).(*tThreadDoor)
}

func completeThread(iSvc string, iTempOk string) {
   var err error
   aRec := _parseTempOk(iTempOk)
   if len(aRec) != 5 {
      fmt.Fprintf(os.Stderr, "completeThread: unexpected file %s%s\n", tempDir(iSvc), iTempOk)
      return
   }
   fmt.Printf("complete %s\n", iTempOk)
   var aFd, aTd *os.File
   if aRec.tid() != "" && aRec.tid() != aRec.mid() {
      aFd, err = os.OpenFile(threadDir(iSvc)+aRec.tid(), os.O_RDWR, 0600)
      if err != nil { quit(err) }
      defer aFd.Close()
      _, err = aFd.Seek(aRec.pos(), io.SeekStart)
      if err != nil { quit(err) }
   }
   aTd, err = os.Open(tempDir(iSvc)+iTempOk)
   if err != nil { quit(err) }
   defer aTd.Close()
   fDraftHead := func(cFlag int8) *tDraftHead {
      cDh := _readDraftHead(aTd)
      if cFlag == 1 && aRec.tid() == aRec.mid() {
         _readCc(aTd, &cDh.cc)
      }
      aTd.Seek(0, io.SeekStart)
      return cDh
   }
   switch aRec.op() {
   case "sr":
      _completeStoreReceived(iSvc, iTempOk, fDraftHead(1), aFd, aTd)
   case "nr":
      _completeSeenReceived(iSvc, iTempOk, aFd, aTd)
   case "ss":
      _completeStoreSent(iSvc, iTempOk, fDraftHead(1), aFd, aTd)
   case "ws":
      _completeStoreDraft(iSvc, iTempOk, fDraftHead(0), aFd, aTd)
   case "ds":
      _completeDeleteDraft(iSvc, iTempOk, aFd, aTd)
   default:
      fmt.Fprintf(os.Stderr, "completeThread: unexpected op %s%s\n", tempDir(iSvc), iTempOk)
   }
}
