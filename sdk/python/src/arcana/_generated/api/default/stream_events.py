from http import HTTPStatus
from typing import Any

import httpx2

from ... import errors
from ...client import AuthenticatedClient, Client
from ...models.error import Error
from ...types import UNSET, Response, Unset


def _get_kwargs(
    *,
    last_event_id: str | Unset = UNSET,
) -> dict[str, Any]:
    headers: dict[str, Any] = {}
    if not isinstance(last_event_id, Unset):
        headers["Last-Event-ID"] = last_event_id

    _kwargs: dict[str, Any] = {
        "method": "get",
        "url": "/v1/events",
    }

    _kwargs["headers"] = headers
    return _kwargs


def _parse_response(
    *, client: AuthenticatedClient | Client, response: httpx2.Response
) -> Error | str | None:
    if response.status_code == 200:
        response_200 = response.text
        return response_200

    if response.status_code == 400:
        response_400 = Error.from_dict(response.json())

        return response_400

    if response.status_code == 401:
        response_401 = Error.from_dict(response.json())

        return response_401

    if response.status_code == 403:
        response_403 = Error.from_dict(response.json())

        return response_403

    if response.status_code == 404:
        response_404 = Error.from_dict(response.json())

        return response_404

    if response.status_code == 409:
        response_409 = Error.from_dict(response.json())

        return response_409

    if response.status_code == 413:
        response_413 = Error.from_dict(response.json())

        return response_413

    if response.status_code == 429:
        response_429 = Error.from_dict(response.json())

        return response_429

    if response.status_code == 500:
        response_500 = Error.from_dict(response.json())

        return response_500

    if response.status_code == 501:
        response_501 = Error.from_dict(response.json())

        return response_501

    if response.status_code == 503:
        response_503 = Error.from_dict(response.json())

        return response_503

    if client.raise_on_unexpected_status:
        raise errors.UnexpectedStatus(response.status_code, response.content)
    else:
        return None


def _build_response(
    *, client: AuthenticatedClient | Client, response: httpx2.Response
) -> Response[Error | str]:
    return Response(
        status_code=HTTPStatus(response.status_code),
        content=response.content,
        headers=response.headers,
        parsed=_parse_response(client=client, response=response),
    )


def sync_detailed(
    *,
    client: AuthenticatedClient,
    last_event_id: str | Unset = UNSET,
) -> Response[Error | str]:
    """Server-Sent Events stream of lifecycle, exec and pool events

     Events carry increasing IDs; reconnect with Last-Event-ID to resume from the last ui.event_buffer
    events. A client that cannot be resumed, or falls too far behind, receives a `reset` event and is
    disconnected and must refetch state. A comment line is sent every 15 seconds. Events are filtered to
    what the caller may see; pool events are admin-only. Types: sandbox.created, sandbox.state,
    sandbox.destroyed, exec.started, exec.ended, pool.changed. Metadata only; exec output is never
    included. See docs/events.md.

    Args:
        last_event_id (str | Unset):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Error | str]
    """

    kwargs = _get_kwargs(
        last_event_id=last_event_id,
    )

    response = client.get_httpx_client().request(
        **kwargs,
    )

    return _build_response(client=client, response=response)


def sync(
    *,
    client: AuthenticatedClient,
    last_event_id: str | Unset = UNSET,
) -> Error | str | None:
    """Server-Sent Events stream of lifecycle, exec and pool events

     Events carry increasing IDs; reconnect with Last-Event-ID to resume from the last ui.event_buffer
    events. A client that cannot be resumed, or falls too far behind, receives a `reset` event and is
    disconnected and must refetch state. A comment line is sent every 15 seconds. Events are filtered to
    what the caller may see; pool events are admin-only. Types: sandbox.created, sandbox.state,
    sandbox.destroyed, exec.started, exec.ended, pool.changed. Metadata only; exec output is never
    included. See docs/events.md.

    Args:
        last_event_id (str | Unset):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Error | str
    """

    return sync_detailed(
        client=client,
        last_event_id=last_event_id,
    ).parsed


async def asyncio_detailed(
    *,
    client: AuthenticatedClient,
    last_event_id: str | Unset = UNSET,
) -> Response[Error | str]:
    """Server-Sent Events stream of lifecycle, exec and pool events

     Events carry increasing IDs; reconnect with Last-Event-ID to resume from the last ui.event_buffer
    events. A client that cannot be resumed, or falls too far behind, receives a `reset` event and is
    disconnected and must refetch state. A comment line is sent every 15 seconds. Events are filtered to
    what the caller may see; pool events are admin-only. Types: sandbox.created, sandbox.state,
    sandbox.destroyed, exec.started, exec.ended, pool.changed. Metadata only; exec output is never
    included. See docs/events.md.

    Args:
        last_event_id (str | Unset):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Error | str]
    """

    kwargs = _get_kwargs(
        last_event_id=last_event_id,
    )

    response = await client.get_async_httpx_client().request(**kwargs)

    return _build_response(client=client, response=response)


async def asyncio(
    *,
    client: AuthenticatedClient,
    last_event_id: str | Unset = UNSET,
) -> Error | str | None:
    """Server-Sent Events stream of lifecycle, exec and pool events

     Events carry increasing IDs; reconnect with Last-Event-ID to resume from the last ui.event_buffer
    events. A client that cannot be resumed, or falls too far behind, receives a `reset` event and is
    disconnected and must refetch state. A comment line is sent every 15 seconds. Events are filtered to
    what the caller may see; pool events are admin-only. Types: sandbox.created, sandbox.state,
    sandbox.destroyed, exec.started, exec.ended, pool.changed. Metadata only; exec output is never
    included. See docs/events.md.

    Args:
        last_event_id (str | Unset):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Error | str
    """

    return (
        await asyncio_detailed(
            client=client,
            last_event_id=last_event_id,
        )
    ).parsed
