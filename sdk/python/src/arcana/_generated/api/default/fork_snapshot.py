from http import HTTPStatus
from typing import Any
from urllib.parse import quote

import httpx2

from ... import errors
from ...client import AuthenticatedClient, Client
from ...models.error import Error
from ...models.fork_request import ForkRequest
from ...models.fork_result import ForkResult
from ...types import Response


def _get_kwargs(
    id: str,
    *,
    body: ForkRequest,
) -> dict[str, Any]:
    headers: dict[str, Any] = {}

    _kwargs: dict[str, Any] = {
        "method": "post",
        "url": "/v1/snapshots/{id}/fork".format(
            id=quote(str(id), safe=""),
        ),
    }

    _kwargs["json"] = body.to_dict()

    headers["Content-Type"] = "application/json"

    _kwargs["headers"] = headers
    return _kwargs


def _parse_response(
    *, client: AuthenticatedClient | Client, response: httpx2.Response
) -> Error | ForkResult | None:
    if response.status_code == 201:
        response_201 = ForkResult.from_dict(response.json())

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
) -> Response[Error | ForkResult]:
    return Response(
        status_code=HTTPStatus(response.status_code),
        content=response.content,
        headers=response.headers,
        parsed=_parse_response(client=client, response=response),
    )


def sync_detailed(
    id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ForkRequest,
) -> Response[Error | ForkResult]:
    """Create N sandboxes from one snapshot

     Returns 201 when at least one sandbox was created; failures lists the others. When none could be
    created the response is the error of the first failure. Each requested sandbox consumes one sandbox-
    creation rate-limit token (at most the per-minute rate).

    Args:
        id (str):
        body (ForkRequest): Overrides apply to every sandbox created by the fork.

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Error | ForkResult]
    """

    kwargs = _get_kwargs(
        id=id,
        body=body,
    )

    response = client.get_httpx_client().request(
        **kwargs,
    )

    return _build_response(client=client, response=response)


def sync(
    id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ForkRequest,
) -> Error | ForkResult | None:
    """Create N sandboxes from one snapshot

     Returns 201 when at least one sandbox was created; failures lists the others. When none could be
    created the response is the error of the first failure. Each requested sandbox consumes one sandbox-
    creation rate-limit token (at most the per-minute rate).

    Args:
        id (str):
        body (ForkRequest): Overrides apply to every sandbox created by the fork.

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Error | ForkResult
    """

    return sync_detailed(
        id=id,
        client=client,
        body=body,
    ).parsed


async def asyncio_detailed(
    id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ForkRequest,
) -> Response[Error | ForkResult]:
    """Create N sandboxes from one snapshot

     Returns 201 when at least one sandbox was created; failures lists the others. When none could be
    created the response is the error of the first failure. Each requested sandbox consumes one sandbox-
    creation rate-limit token (at most the per-minute rate).

    Args:
        id (str):
        body (ForkRequest): Overrides apply to every sandbox created by the fork.

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Error | ForkResult]
    """

    kwargs = _get_kwargs(
        id=id,
        body=body,
    )

    response = await client.get_async_httpx_client().request(**kwargs)

    return _build_response(client=client, response=response)


async def asyncio(
    id: str,
    *,
    client: AuthenticatedClient | Client,
    body: ForkRequest,
) -> Error | ForkResult | None:
    """Create N sandboxes from one snapshot

     Returns 201 when at least one sandbox was created; failures lists the others. When none could be
    created the response is the error of the first failure. Each requested sandbox consumes one sandbox-
    creation rate-limit token (at most the per-minute rate).

    Args:
        id (str):
        body (ForkRequest): Overrides apply to every sandbox created by the fork.

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Error | ForkResult
    """

    return (
        await asyncio_detailed(
            id=id,
            client=client,
            body=body,
        )
    ).parsed
