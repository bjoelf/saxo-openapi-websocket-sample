This project is a simple saxo websocket demo inplementation. It use a Saxo provided 24H token

Saxo has a similar sample written in C#:  (https://github.com/SaxoBank/openapi-samples-csharp/tree/master/websockets)

A few sample subscriptions is also provided. Two price subscription, one for FX prices and one for contract futures. A subscription for orders and one for portfolio balances. These subscriptions should demonstrade enough code to further implement any subscription available.  

The connection runs for 2 minutes and makes a graceful termination.