// Copyright 2017 Liam Breck
//
// This file is part of the "mnm" software. Anyone may redistribute mnm and/or modify
// it under the terms of the GNU Lesser General Public License version 3, as published
// by the Free Software Foundation. See www.gnu.org/licenses/
// Mnm is distributed WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See said License for details.

package slib

import (
   "io"
   "io/ioutil"
   "encoding/json"
   "os"
   "sort"
   "strings"
   "time"
)

func WriteResultSearch(iW io.Writer, iSvc string, iState *ClientState) error {
   var err error
   aTabType, aTabVal := iState.getSvcTab()
   if aTabType == ePosForTerms && strings.HasPrefix(aTabVal, "ffn:") {
      aFfn := aTabVal[4:]
      _, err = iW.Write([]byte(`{"Ffn":`))
      if err != nil { return err }
      err = json.NewEncoder(iW).Encode(aFfn)
      if err != nil { return err }
      _, err = iW.Write([]byte(`,"Table":`))
      if err != nil { return err }
      err = WriteTableFilledForm(iW, iSvc, aFfn)
      if err != nil { return err }
      _, err = iW.Write([]byte{'}'})
      return err
   }
   if aTabType != ePosForDefault {
      _, err = iW.Write([]byte(`[]`))
      return err
   }
   var aDir []os.FileInfo
   if aTabVal == "FFT" {
      aDir, err = ioutil.ReadDir(dirForm(iSvc))
      if err != nil { quit(err) }
   } else {
      aDir, err = ioutil.ReadDir(dirThread(iSvc))
      if err != nil { quit(err) }
      sort.Slice(aDir, func(cA, cB int) bool { return aDir[cA].ModTime().After(aDir[cB].ModTime()) })
   }
   type tSearchEl struct { Id, Date string }
   aList := make([]tSearchEl, 0, len(aDir))
   for _, aFi := range aDir {
      aDate := aFi.ModTime().UTC().Format(time.RFC3339)
      if aTabVal == "FFT" {
         aList = append(aList, tSearchEl{Date:aDate, Id: strings.Replace(aFi.Name(), "@", "/", -1)})
      } else if !strings.ContainsRune(aFi.Name()[1:], '_') {
         aList = append(aList, tSearchEl{Date:aDate, Id: aFi.Name()})
      }
   }
   err = json.NewEncoder(iW).Encode(aList)
   return err
}

