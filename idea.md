# gRPC Proxy

## Background

I want to create a gRPC reverse proxy that is message aware, it should completely terminate a gRPC stream, and forward the message to another gRPC server, and forward the response to the original client. 

It should work for any RPC and should be able to handle any message type.
It should be able to inspect the message types

It should have a way to declare what RPCs and types it should proxy.

New RPCs and messages do not require a code change to the proxy.


## Requirements

- Must be written in golang
- Must work with Any RPC
- Must work with Any Message Type
- Must be able to inspect the message types
- Must be able to declare what RPCs and types it should proxy
- Must be able to handle new RPCs and messages without a code change

