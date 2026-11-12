import datetime
from http import HTTPStatus
from typing import Any

import httpx2

from ... import errors
from ...client import AuthenticatedClient, Client
from ...models.audit_list import AuditList
from ...models.error import Error
from ...types import UNSET, Response, Unset


def _get_kwargs(
    *,
    sandbox_id: str | Unset = UNSET,
    key_id: str | Unset = UNSET,
    action: str | Unset = UNSET,
    since: datetime.datetime | Unset = UNSET,
    until: datetime.datetime | Unset = UNSET,
    cursor: str | Unset = UNSET,
    limit: int | Unset = UNSET,
) -> dict[str, Any]:

    params: dict[str, Any] = {}

    params["sandbox_id"] = sandbox_id

    params["key_id"] = key_id

    params["action"] = action

    json_since: str | Unset = UNSET
    if not isinstance(since, Unset):
        json_since = since.isoformat()
    params["since"] = json_since

    json_until: str | Unset = UNSET
    if not isinstance(until, Unset):
        json_until = until.isoformat()
    params["until"] = json_until

    params["cursor"] = cursor

    params["limit"] = limit

    params = {k: v for k, v in params.items() if v is not UNSET and v is not None}

    _kwargs: dict[str, Any] = {
        "method": "get",
        "url": "/v1/audit",
        "params": params,
    }

    return _kwargs


def _parse_response(
    *, client: AuthenticatedClient | Client, response: httpx2.Response
) -> AuditList | Error | None:
    if response.status_code == 200:
        response_200 = AuditList.from_dict(response.json())

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

    if client.raise_on_unexpected_status:
        raise errors.UnexpectedStatus(response.status_code, response.content)
    else:
        return None


def _build_response(
    *, client: AuthenticatedClient | Client, response: httpx2.Response
) -> Response[AuditList | Error]:
    return Response(
        status_code=HTTPStatus(response.status_code),
        content=response.content,
        headers=response.headers,
        parsed=_parse_response(client=client, response=response),
    )


def sync_detailed(
    *,
    client: AuthenticatedClient | Client,
    sandbox_id: str | Unset = UNSET,
    key_id: str | Unset = UNSET,
    action: str | Unset = UNSET,
    since: datetime.datetime | Unset = UNSET,
    until: datetime.datetime | Unset = UNSET,
    cursor: str | Unset = UNSET,
    limit: int | Unset = UNSET,
) -> Response[AuditList | Error]:
    """Query audit events (admin only)

    Args:
        sandbox_id (str | Unset):
        key_id (str | Unset):
        action (str | Unset):
        since (datetime.datetime | Unset):
        until (datetime.datetime | Unset):
        cursor (str | Unset):
        limit (int | Unset):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[AuditList | Error]
    """

    kwargs = _get_kwargs(
        sandbox_id=sandbox_id,
        key_id=key_id,
        action=action,
        since=since,
        until=until,
        cursor=cursor,
        limit=limit,
    )

    response = client.get_httpx_client().request(
        **kwargs,
    )

    return _build_response(client=client, response=response)


def sync(
    *,
    client: AuthenticatedClient | Client,
    sandbox_id: str | Unset = UNSET,
    key_id: str | Unset = UNSET,
    action: str | Unset = UNSET,
    since: datetime.datetime | Unset = UNSET,
    until: datetime.datetime | Unset = UNSET,
    cursor: str | Unset = UNSET,
    limit: int | Unset = UNSET,
) -> AuditList | Error | None:
    """Query audit events (admin only)

    Args:
        sandbox_id (str | Unset):
        key_id (str | Unset):
        action (str | Unset):
        since (datetime.datetime | Unset):
        until (datetime.datetime | Unset):
        cursor (str | Unset):
        limit (int | Unset):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        AuditList | Error
    """

    return sync_detailed(
        client=client,
        sandbox_id=sandbox_id,
        key_id=key_id,
        action=action,
        since=since,
        until=until,
        cursor=cursor,
        limit=limit,
    ).parsed


async def asyncio_detailed(
    *,
    client: AuthenticatedClient | Client,
    sandbox_id: str | Unset = UNSET,
    key_id: str | Unset = UNSET,
    action: str | Unset = UNSET,
    since: datetime.datetime | Unset = UNSET,
    until: datetime.datetime | Unset = UNSET,
    cursor: str | Unset = UNSET,
    limit: int | Unset = UNSET,
) -> Response[AuditList | Error]:
    """Query audit events (admin only)

    Args:
        sandbox_id (str | Unset):
        key_id (str | Unset):
        action (str | Unset):
        since (datetime.datetime | Unset):
        until (datetime.datetime | Unset):
        cursor (str | Unset):
        limit (int | Unset):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        Response[AuditList | Error]
    """

    kwargs = _get_kwargs(
        sandbox_id=sandbox_id,
        key_id=key_id,
        action=action,
        since=since,
        until=until,
        cursor=cursor,
        limit=limit,
    )

    response = await client.get_async_httpx_client().request(**kwargs)

    return _build_response(client=client, response=response)


async def asyncio(
    *,
    client: AuthenticatedClient | Client,
    sandbox_id: str | Unset = UNSET,
    key_id: str | Unset = UNSET,
    action: str | Unset = UNSET,
    since: datetime.datetime | Unset = UNSET,
    until: datetime.datetime | Unset = UNSET,
    cursor: str | Unset = UNSET,
    limit: int | Unset = UNSET,
) -> AuditList | Error | None:
    """Query audit events (admin only)

    Args:
        sandbox_id (str | Unset):
        key_id (str | Unset):
        action (str | Unset):
        since (datetime.datetime | Unset):
        until (datetime.datetime | Unset):
        cursor (str | Unset):
        limit (int | Unset):

    Raises:
        errors.UnexpectedStatus: If the server returns an undocumented status code and Client.raise_on_unexpected_status is True.
        httpx2.TimeoutException: If the request takes longer than Client.timeout.

    Returns:
        AuditList | Error
    """

    return (
        await asyncio_detailed(
            client=client,
            sandbox_id=sandbox_id,
            key_id=key_id,
            action=action,
            since=since,
            until=until,
            cursor=cursor,
            limit=limit,
        )
    ).parsed
