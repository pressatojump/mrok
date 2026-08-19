#!/bin/bash
# Launch mrok on the cheapest EC2 size: t4g.nano (Graviton, 2 vCPU, 0.5 GiB).
# Usage: deploy/aws.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
NAME="${MROK_INSTANCE_NAME:-mrok}"
TYPE="${MROK_INSTANCE_TYPE:-t4g.nano}"
KEY_NAME="${MROK_KEY_NAME:-mrok}"
KEY_FILE="${MROK_KEY_FILE:-$HOME/.ssh/${KEY_NAME}-aws.pem}"
SG_NAME="${MROK_SG_NAME:-mrok}"

export AWS_DEFAULT_REGION="$REGION"
export AWS_PAGER=""

need() { command -v "$1" >/dev/null || { echo "need $1" >&2; exit 1; }; }
need aws
need go
need ssh
need scp

echo "region=$REGION type=$TYPE"

AMI="$(aws ssm get-parameters \
  --names /aws/service/ami-amazon-linux-latest/al2023-ami-minimal-kernel-default-arm64 \
  --query 'Parameters[0].Value' --output text)"
echo "ami=$AMI"

VPC="$(aws ec2 describe-vpcs --filters Name=isDefault,Values=true \
  --query 'Vpcs[0].VpcId' --output text)"
[ "$VPC" != None ] && [ -n "$VPC" ] || { echo "no default VPC" >&2; exit 1; }

if ! aws ec2 describe-key-pairs --key-names "$KEY_NAME" >/dev/null 2>&1; then
  mkdir -p "$(dirname "$KEY_FILE")"
  aws ec2 create-key-pair --key-name "$KEY_NAME" --query 'KeyMaterial' --output text > "$KEY_FILE"
  chmod 600 "$KEY_FILE"
  echo "created key $KEY_NAME -> $KEY_FILE"
fi
[ -f "$KEY_FILE" ] || { echo "key pair $KEY_NAME exists in AWS but $KEY_FILE is missing" >&2; exit 1; }

SG="$(aws ec2 describe-security-groups --filters Name=group-name,Values="$SG_NAME" Name=vpc-id,Values="$VPC" \
  --query 'SecurityGroups[0].GroupId' --output text 2>/dev/null || true)"
if [ -z "$SG" ] || [ "$SG" = None ]; then
  SG="$(aws ec2 create-security-group --group-name "$SG_NAME" --description "mrok relay" \
    --vpc-id "$VPC" --query GroupId --output text)"
  aws ec2 authorize-security-group-ingress --group-id "$SG" --ip-permissions \
    'IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=0.0.0.0/0}]' \
    'IpProtocol=tcp,FromPort=80,ToPort=80,IpRanges=[{CidrIp=0.0.0.0/0}]' \
    'IpProtocol=tcp,FromPort=443,ToPort=443,IpRanges=[{CidrIp=0.0.0.0/0}]' \
    'IpProtocol=tcp,FromPort=20000,ToPort=20031,IpRanges=[{CidrIp=0.0.0.0/0}]' >/dev/null
  echo "created sg $SG"
fi

EXISTING="$(aws ec2 describe-instances \
  --filters "Name=tag:Name,Values=$NAME" "Name=instance-state-name,Values=pending,running,stopping,stopped" \
  --query 'Reservations[].Instances[0].InstanceId' --output text)"
if [ -n "$EXISTING" ] && [ "$EXISTING" != None ]; then
  ID="$(echo "$EXISTING" | awk '{print $1}')"
  echo "reusing instance $ID"
  STATE="$(aws ec2 describe-instances --instance-ids "$ID" --query 'Reservations[0].Instances[0].State.Name' --output text)"
  if [ "$STATE" = stopped ] || [ "$STATE" = stopping ]; then
    aws ec2 start-instances --instance-ids "$ID" >/dev/null
    aws ec2 wait instance-running --instance-ids "$ID"
  fi
else
  AZS="$(aws ec2 describe-instance-type-offerings --location-type availability-zone \
    --filters Name=instance-type,Values="$TYPE" --query 'InstanceTypeOfferings[].Location' --output text)"
  ID=""
  for AZ in $AZS; do
    SUBNET="$(aws ec2 describe-subnets --filters Name=vpc-id,Values="$VPC" Name=availability-zone,Values="$AZ" \
      --query 'Subnets[0].SubnetId' --output text)"
    [ -n "$SUBNET" ] && [ "$SUBNET" != None ] || continue
    if ID="$(aws ec2 run-instances \
      --image-id "$AMI" \
      --instance-type "$TYPE" \
      --key-name "$KEY_NAME" \
      --security-group-ids "$SG" \
      --subnet-id "$SUBNET" \
      --associate-public-ip-address \
      --block-device-mappings '[{"DeviceName":"/dev/xvda","Ebs":{"VolumeSize":8,"VolumeType":"gp3","DeleteOnTermination":true}}]' \
      --user-data "file://$ROOT/deploy/user-data.sh" \
      --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=$NAME},{Key=app,Value=mrok}]" \
      --query 'Instances[0].InstanceId' --output text 2>/dev/null)"; then
      echo "launched $ID in $AZ"
      break
    fi
    ID=""
  done
  [ -n "$ID" ] || { echo "failed to launch $TYPE in any AZ" >&2; exit 1; }
  aws ec2 wait instance-running --instance-ids "$ID"
fi

ALLOC="$(aws ec2 describe-addresses --filters "Name=tag:Name,Values=$NAME" \
  --query 'Addresses[0].AllocationId' --output text)"
if [ -z "$ALLOC" ] || [ "$ALLOC" = None ]; then
  ALLOC="$(aws ec2 allocate-address --domain vpc --query AllocationId --output text)"
  aws ec2 create-tags --resources "$ALLOC" --tags Key=Name,Value="$NAME" Key=app,Value=mrok
fi
ASSOC="$(aws ec2 describe-addresses --allocation-ids "$ALLOC" --query 'Addresses[0].InstanceId' --output text)"
if [ "$ASSOC" != "$ID" ]; then
  aws ec2 associate-address --instance-id "$ID" --allocation-id "$ALLOC" --allow-reassociation >/dev/null
fi
IP="$(aws ec2 describe-addresses --allocation-ids "$ALLOC" --query 'Addresses[0].PublicIp' --output text)"
echo "public ip $IP"

echo "building linux/arm64"
mkdir -p "$ROOT/bin"
( cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
  -ldflags "-s -w -X main.version=$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev) -X main.defaultServer=https://${IP}:443" \
  -o "$ROOT/bin/mrok-linux-arm64" . )

echo "waiting for ssh on $IP"
for i in $(seq 1 36); do
  if ssh -i "$KEY_FILE" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=5 -o BatchMode=yes \
      ec2-user@"$IP" 'echo up' >/dev/null 2>&1; then
    break
  fi
  sleep 5
done

ssh -i "$KEY_FILE" -o StrictHostKeyChecking=accept-new ec2-user@"$IP" 'sudo mkdir -p /etc/mrok /var/lib/mrok /usr/local/bin'
scp -i "$KEY_FILE" -o StrictHostKeyChecking=accept-new \
  "$ROOT/bin/mrok-linux-arm64" ec2-user@"$IP":/tmp/mrok
scp -i "$KEY_FILE" -o StrictHostKeyChecking=accept-new \
  "$ROOT/deploy/mrok.service" ec2-user@"$IP":/tmp/mrok.service
ssh -i "$KEY_FILE" -o BatchMode=yes ec2-user@"$IP" "sudo bash -s $IP" <<'EOS'
set -euo pipefail
IP="$1"
install -m 0755 /tmp/mrok /usr/local/bin/mrok
install -m 0644 /tmp/mrok.service /etc/systemd/system/mrok.service
# advertise this box's public IP to clients
mkdir -p /etc/systemd/system/mrok.service.d
cat >/etc/systemd/system/mrok.service.d/override.conf <<EOF
[Service]
Environment=MROK_PUBLIC_IP=$IP
EOF
if [ ! -f /etc/mrok/token ]; then
  umask 077
  dd if=/dev/urandom bs=24 count=1 2>/dev/null | od -An -tx1 | tr -d ' \n' > /etc/mrok/token
  echo >> /etc/mrok/token
fi
systemctl daemon-reload
systemctl enable --now mrok
systemctl restart mrok
EOS

sleep 2
ssh -i "$KEY_FILE" -o BatchMode=yes ec2-user@"$IP" 'systemctl is-active mrok && curl -fsS http://127.0.0.1/healthz'

printf 'https://%s:443\n' "$IP" > "$ROOT/endpoint"
echo
echo "mrok is up"
echo "  public   https://<id>.$(echo "$IP" | tr . -).sslip.io"
echo "  control  https://${IP}:443"
echo "  health   http://${IP}/healthz"
echo "  token    ssh -i $KEY_FILE ec2-user@$IP 'sudo cat /etc/mrok/token'"
echo "  install  curl -fsSL https://raw.githubusercontent.com/pressatojump/mrok/main/install.sh | sh"
echo
echo "wrote $ROOT/endpoint — commit it so the installer pins this relay"
