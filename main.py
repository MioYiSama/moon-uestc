import os
from datetime import datetime

from moon_uestc import MoonClient
from moon_uestc.models import CheckinRequest

with MoonClient(session=os.environ["MOON_SESSION"], checkin_ticket="1") as client:
    address = client.student.reverse_geocode(
        latitude=30.68,
        longitude=104.10,
    ).data
    print(address)
    result = client.student.checkin(
        CheckinRequest(
            client_time=datetime.now(),
            latitude=30.68,
            longitude=104.10,
            reverse_geocode=address,
        )
    )

    print(result.model_dump_json())
