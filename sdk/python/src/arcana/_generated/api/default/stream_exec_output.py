from http import HTTPStatus
from typing import Any
from urllib.parse import quote

import httpx2

from ... import errors
from ...client import AuthenticatedClient, Client
from ...models.error import Error
from ...types import Response


def _get_kwargs(
    id: str,
    exec_id: str,
) -> dict[str, Any]:

    _kwargs: dict[str, Any] = {
        "method": "get",
        "url": "/v1/sandboxes/{id}/execs/{exec_id}/output".format(
            id=quote(str(id), safe=""),
            exec_id=quote(str(exec_id), safe=""),
        ),
    }

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
    id: str,
    exec_id: str,
    *,
    client: AuthenticatedClient,
) -> Response[Error | str]:
    """Server-Sent Events tail of an exec's buffered and live output

     Replays the in-memory ring (limits.exec.tail_buffer_bytes), then streams live output until the exec
    ends. `output` events carry {seq, stream, data} with base64 data; `gap` means older output was
    discarded; `end` carries the exit result, with expired=true when the output is no longer retained
    (tail_retention_seconds after the exec ended). Output is never persisted. At most
    limits.exec.max_tail_subscribers readers per exec.

    Args:
        id (str):
        exec_id (str):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Error | str]
    """

    kwargs = _get_kwargs(
        id=id,
        exec_id=exec_id,
    )

    response = client.get_httpx_client().request(
        **kwargs,
    )

    return _build_response(client=client, response=response)


def sync(
    id: str,
    exec_id: str,
    *,
    client: AuthenticatedClient,
) -> Error | str | None:
    """Server-Sent Events tail of an exec's buffered and live output

     Replays the in-memory ring (limits.exec.tail_buffer_bytes), then streams live output until the exec
    ends. `output` events carry {seq, stream, data} with base64 data; `gap` means older output was
    discarded; `end` carries the exit result, with expired=true when the output is no longer retained
    (tail_retention_seconds after the exec ended). Output is never persisted. At most
    limits.exec.max_tail_subscribers readers per exec.

    Args:
        id (str):
        exec_id (str):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Error | str
    """

    return sync_detailed(
        id=id,
        exec_id=exec_id,
        client=client,
    ).parsed


async def asyncio_detailed(
    id: str,
    exec_id: str,
    *,
    client: AuthenticatedClient,
) -> Response[Error | str]:
    """Server-Sent Events tail of an exec's buffered and live output

     Replays the in-memory ring (limits.exec.tail_buffer_bytes), then streams live output until the exec
    ends. `output` events carry {seq, stream, data} with base64 data; `gap` means older output was
    discarded; `end` carries the exit result, with expired=true when the output is no longer retained
    (tail_retention_seconds after the exec ended). Output is never persisted. At most
    limits.exec.max_tail_subscribers readers per exec.

    Args:
        id (str):
        exec_id (str):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[Error | str]
    """

    kwargs = _get_kwargs(
        id=id,
        exec_id=exec_id,
    )

    response = await client.get_async_httpx_client().request(**kwargs)

    return _build_response(client=client, response=response)


async def asyncio(
    id: str,
    exec_id: str,
    *,
    client: AuthenticatedClient,
) -> Error | str | None:
    """Server-Sent Events tail of an exec's buffered and live output

     Replays the in-memory ring (limits.exec.tail_buffer_bytes), then streams live output until the exec
    ends. `output` events carry {seq, stream, data} with base64 data; `gap` means older output was
    discarded; `end` carries the exit result, with expired=true when the output is no longer retained
    (tail_retention_seconds after the exec ended). Output is never persisted. At most
    limits.exec.max_tail_subscribers readers per exec.

    Args:
        id (str):
        exec_id (str):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Error | str
    """

    return (
        await asyncio_detailed(
            id=id,
            exec_id=exec_id,
            client=client,
        )
    ).parsed
