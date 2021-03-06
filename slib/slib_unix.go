// Copyright 2019 Liam Breck
// Published at https://github.com/networkimprov/mnm-hammer
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/

// +build linux darwin

package slib

import (
   "os"
   "syscall"
)

const kENOTEMPTY = syscall.ENOTEMPTY

func getInode(_ string, iFi os.FileInfo) (uint64, error) {
   return iFi.Sys().(*syscall.Stat_t).Ino, nil
}

func syncDir(iPath string) error {
   aFd, err := os.Open(iPath)
   if err != nil { return err }
   err = aFd.Sync()
   aFd.Close()
   return err
}

func syncTree(iPath string) error {
   if iPath[len(iPath)-1] == '/' {
      iPath = iPath[:len(iPath)-1]
   }
   err := syncDir(iPath)
   if err != nil { return err }
   aDir, err := readDirFis(iPath)
   if err != nil { return err }
   for _, aFi := range aDir {
      if !aFi.IsDir() { continue }
      err = syncTree(iPath +"/"+ aFi.Name())
      if err != nil { return err }
   }
   return nil
}
