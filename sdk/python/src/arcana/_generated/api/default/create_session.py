from http import HTTPStatus
from typing import Any

import httpx2

from ... import errors
from ...client import AuthenticatedClient, Client
from ...models.error import Error
from ...models.session import Session
from ...types import Response


def _get_kwargs() -> dict[str, Any]:

    _kwargs: dict[str, Any] = {
        "method": "post",
        "url": "/v1/sessions",
    }

    return _kwargs


def _parse_response(
    *, client: AuthenticatedClient | Client, response: httpx2.Response
) -> Error | Session | None:
    if response.status_code == 201:
        response_201 = Session.from_dict(response.json())

        return response_201

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
) -> Response[Error | Session]:
    return Response(
        status_code=HTTPStatus(response.status_code),
        content=response.content,
        headers=response.headers,
        parsed=_parse_response(client=client, response=response),
    )


def sync_detailed(
    *,
    client: AuthenticatedClient | Client,
) -> Response[Error | Session]:
    """Exchange the API key for a dashboard session cookie

     Authenticated with an API key (Authorization header); a cookie cannot create sessions. The response
    sets the session token as an HttpOnly, SameSite=Strict cookie, Secure unless the request is plain
    HTTP to a loopback address. Only the token's SHA-256 is stored. Sessions expire after
    ui.session_ttl_seconds and stop working when the key is revoked. Each key keeps at most 20 sessions.

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Error | Session]
    """

    kwargs = _get_kwargs()

    response = client.get_httpx_client().request(
        **kwargs,
    )

    return _build_response(client=client, response=response)


def sync(
    *,
    client: AuthenticatedClient | Client,
) -> Error | Session | None:
    """Exchange the API key for a dashboard session cookie

     Authenticated with an API key (Authorization header); a cookie cannot create sessions. The response
    sets the session token as an HttpOnly, SameSite=Strict cookie, Secure unless the request is plain
    HTTP to a loopback address. Only the token's SHA-256 is stored. Sessions expire after
    ui.session_ttl_seconds and stop working when the key is revoked. Each key keeps at most 20 sessions.

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Error | Session
    """

    return sync_detailed(
        client=client,
    ).parsed


async def asyncio_detailed(
    *,
    client: AuthenticatedClient | Client,
) -> Response[Error | Session]:
    """Exchange the API key for a dashboard session cookie

     Authenticated with an API key (Authorization header); a cookie cannot create sessions. The response
    sets the session token as an HttpOnly, SameSite=Strict cookie, Secure unless the request is plain
    HTTP to a loopback address. Only the token's SHA-256 is stored. Sessions expire after
    ui.session_ttl_seconds and stop working when the key is revoked. Each key keeps at most 20 sessions.

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Error | Session]
    """

    kwargs = _get_kwargs()

    response = await client.get_async_httpx_client().request(**kwargs)

    return _build_response(client=client, response=response)


async def asyncio(
    *,
    client: AuthenticatedClient | Client,
) -> Error | Session | None:
    """Exchange the API key for a dashboard session cookie

     Authenticated with an API key (Authorization header); a cookie cannot create sessions. The response
    sets the session token as an HttpOnly, SameSite=Strict cookie, Secure unless the request is plain
    HTTP to a loopback address. Only the token's SHA-256 is stored. Sessions expire after
    ui.session_ttl_seconds and stop working when the key is revoked. Each key keeps at most 20 sessions.

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Error | Session
    """

    return (
        await asyncio_detailed(
            client=client,
        )
    ).parsed
