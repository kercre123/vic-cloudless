#!/usr/bin/env bash

TOOLCHAIN_VER="5.3.0-r07"

if [[ ! `which pv` ]]; then
  echo "Install "pv" via your os's package manager"
  echo "
  # Arch Linux:
  sudo pacman -S pv
  # Ubuntu/Debian:
  sudo apt-get update && sudo apt-get install -y pv
  # Fedora
  sudo dnf install -y pv"
  exit 1
fi

if [[ ! -d ~/.anki/vicos-sdk/dist/$TOOLCHAIN_VER ]]; then
  echo Getting toolchain version $TOOLCHAIN_VER...
  mkdir ~/.anki/vicos-sdk/dist/$TOOLCHAIN_VER
  cd ~/.anki/vicos-sdk/dist/$TOOLCHAIN_VER
  wget https://github.com/os-vector/wire-os-externals/releases/download/$TOOLCHAIN_VER/vicos-sdk_"$TOOLCHAIN_VER"_amd64-linux.tar.gz
  pv vicos-sdk_"$TOOLCHAIN_VER"_amd64-linux.tar.gz | tar -xz
  rm vicos-sdk_"$TOOLCHAIN_VER"_amd64-linux.tar.gz
  echo "Toolchain version $TOOLCHAIN_VER has been installed!"
  exit
else
  echo "Toolchain version $TOOLCHAIN_VER is already installed!"
  exit
fi
