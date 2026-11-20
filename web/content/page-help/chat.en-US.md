# Chat Page Data Guide

This page is the built-in “Trial Chat” window for talking directly with a model connected to the gateway. It is also the quickest way to verify whether your key is working.

## Prerequisites

You need an API key **that belongs to you and is enabled**. If you do not have a key, the page will guide you to request one first. If the key is disabled or has expired, the chat will fail and display the reason.

## Model Selection

- Select “auto” to have the gateway automatically choose a suitable model based on the routing strategy.
- If you manually select a model, that choice is used. If the model is unavailable, the current chat turn will fail, but the next turn is not affected.
- **Parameter changes take effect on the next turn**: changes to temperature or model are applied starting with your next message, while historical messages remain unchanged.

## Relationship to Statistics

- Every chat turn here is a real call and **will count toward usage and costs**—testing also costs money.
- Chat records correspond one-to-one with records on the “Request Records” page: you can find the details for each turn in the request records.

## How to Self-Check When the Numbers Do Not Match

1. The costs of two turns in the same conversation differ significantly: in auto mode, a model is selected independently for each turn, so a more or less expensive model may have been used.
2. A response is truncated: first check the “Max Output Length” setting, then check the selected model’s capacity limit.
3. The chat fails, but the key page appears normal: confirm that the key’s quota has not been exhausted and that the model is available for your key.
