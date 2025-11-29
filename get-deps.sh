#!/usr/bin/env bash

TOOLCHAIN_VER="5.3.0-r07"

if [[ ! -d ~/.anki/vicos-sdk/dist/$TOOLCHAIN_VER ]]; then
  echo "Getting toolchain version $TOOLCHAIN_VER..."
  mkdir -p ~/.anki/vicos-sdk/dist/$TOOLCHAIN_VER
  cd ~/.anki/vicos-sdk/dist/$TOOLCHAIN_VER
  wget -q --show-progress https://github.com/os-vector/wire-os-externals/releases/download/$TOOLCHAIN_VER/vicos-sdk_"$TOOLCHAIN_VER"_amd64-linux.tar.gz -O - | tar -xz
  echo "Toolchain version $TOOLCHAIN_VER has been installed!"
  exit
else
  echo "Toolchain version $TOOLCHAIN_VER is already installed!"
  exit
fi
