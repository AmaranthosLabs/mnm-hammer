// Copyright 2017, 2019 Liam Breck
// Published at https://github.com/networkimprov/mnm-hammer
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/

package slib

import (
   "time"
)

const kNoticeMaxHours = 7 * 24 + 1


type tNoticeEl struct {
   Type string
   MsgId string
   Date string
   Seen uint32
   Alias string
   Gid string `json:",omitempty"`
   Blurb string `json:",omitempty"`
}

func GetIdxNotice(iSvc string) []tNoticeEl {
   aSvc := getService(iSvc)
   aSvc.RLock(); defer aSvc.RUnlock()
   aIdx := append([]tNoticeEl{}, aSvc.notice...)
   for a1, a2 := 0, len(aIdx)-1; a1 < a2; a1, a2 = a1+1, a2-1 {
      aIdx[a1], aIdx[a2] = aIdx[a2], aIdx[a1]
   }
   return aIdx
}

func addPingNotice(iSvc string, iMsgId string, iAlias, iGid string, iBlurb string) {
   aSvc := getService(iSvc)
   aSvc.Lock(); defer aSvc.Unlock()
   for a := range aSvc.notice {
      if aSvc.notice[a].MsgId == iMsgId {
         return
      }
   }
   aEl := tNoticeEl{Type:"i", MsgId:iMsgId, Date:dateRFC3339(), Alias:iAlias, Gid:iGid, Blurb:iBlurb}
   aSvc.notice = append(aSvc.notice, aEl)
   err := storeFile(fileNotc(iSvc), aSvc.notice)
   if err != nil { quit(err) }
}

func setLastSeenNotice(iSvc string, iUpdt *Update) error {
   if iUpdt.Notice.MsgId == "" {
      return tError("msgid missing")
   }
   aSvc := getService(iSvc)
   aSvc.Lock(); defer aSvc.Unlock()
   if len(aSvc.notice) == 0 {
      return tError("notice list empty")
   }
   var err error
   aSeenSet := aSvc.notice[0].Seen
   aDate, aNow := time.Time{}, time.Now()
   aStart := 0
   for a := range aSvc.notice { // keep at least one seenset
      if aSvc.notice[a].Seen > 0 {
         if aSvc.notice[a].Seen != aSeenSet {
            aDate, err = time.Parse(time.RFC3339, aSvc.notice[a-1].Date)
            if err != nil { quit(err) }
            if aNow.Sub(aDate) > kNoticeMaxHours * time.Hour {
               aStart = a
            }
            aSeenSet = aSvc.notice[a].Seen
         }
      } else {
         aSvc.notice[a].Seen = aSeenSet + 1
      }
      if aSvc.notice[a].MsgId == iUpdt.Notice.MsgId {
         break
      }
   }
   aSvc.notice = aSvc.notice[aStart:]
   err = storeFile(fileNotc(iSvc), aSvc.notice)
   if err != nil { quit(err) }
   return nil
}

